package ui

import (
	"strings"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
)

type commandID int

const (
	commandConnect commandID = iota
	commandDisconnect
	commandBrowseIdentity
	commandRefresh
	commandToggleHidden
	commandTheme
	commandSettings
	commandUpload
	commandDownload
	commandCancelTransfers
	commandResetLayout
	commandHelp
	commandNewFolder
	commandRename
	commandDelete
	commandEditFile
)

type paletteCommand struct {
	id    commandID
	title string
	hint  string
}

func (m *Model) openCommandPalette() {
	m.commandQuery = ""
	m.commandCursor = 0
	m.overlay = overlayCommandPalette
	m.setStatus("command palette")
}

func (m Model) paletteCommands() []paletteCommand {
	commands := []paletteCommand{
		{id: commandConnect, title: "Connect", hint: "pick a saved server or add one"},
		{id: commandRefresh, title: "Refresh", hint: "reload visible panes"},
		{id: commandNewFolder, title: "New folder", hint: "create folder in focused pane"},
		{id: commandToggleHidden, title: "Toggle hidden files", hint: "show or hide dotfiles"},
		{id: commandTheme, title: "Theme picker", hint: "choose color theme"},
		{id: commandSettings, title: "Settings", hint: "open app settings"},
		{id: commandResetLayout, title: "Reset layout", hint: "restore pane sizes"},
		{id: commandHelp, title: "Help", hint: "show keyboard reference"},
	}
	if m.conn != nil {
		commands = append(commands, paletteCommand{id: commandDisconnect, title: "Disconnect", hint: "close current connection"})
	}
	if connectProtocols[m.connectForm.protocol] == "sftp" || m.target.Protocol == "sftp" || len(m.targets) == 0 {
		commands = append(commands, paletteCommand{id: commandBrowseIdentity, title: "Browse identity file", hint: "choose an SFTP key"})
	}
	if m.connected() && len(m.local.actionEntries()) > 0 {
		commands = append(commands, paletteCommand{id: commandUpload, title: "Upload selected", hint: "queue local selection"})
	}
	if m.connected() && len(m.remote.actionEntries()) > 0 {
		commands = append(commands, paletteCommand{id: commandDownload, title: "Download selected", hint: "queue remote selection"})
	}
	if hasCancelableTransfer(m.transfers) {
		commands = append(commands, paletteCommand{id: commandCancelTransfers, title: "Cancel active transfers", hint: "stop current work"})
	}
	if pane := m.focusedFilePane(); pane != nil && len(pane.actionEntries()) > 0 {
		commands = append(commands,
			paletteCommand{id: commandRename, title: "Rename item", hint: "rename highlighted item"},
			paletteCommand{id: commandDelete, title: "Delete item(s)", hint: "delete selection or highlight"},
		)
		if entry, ok := pane.current(); ok && !entry.IsDir() && !isParentDirEntry(entry) {
			commands = append(commands, paletteCommand{id: commandEditFile, title: "Edit file", hint: "open in $EDITOR"})
		}
	}
	return commands
}

func hasCancelableTransfer(transfers []domain.Transfer) bool {
	for _, transfer := range transfers {
		if transfer.Status == domain.Active || transfer.Status == domain.Queued {
			return true
		}
	}
	return false
}

func (m Model) filteredPaletteCommands() []paletteCommand {
	commands := m.paletteCommands()
	query := strings.ToLower(strings.TrimSpace(m.commandQuery))
	if query == "" {
		return commands
	}
	filtered := make([]paletteCommand, 0, len(commands))
	for _, command := range commands {
		haystack := strings.ToLower(command.title + " " + command.hint)
		if strings.Contains(haystack, query) {
			filtered = append(filtered, command)
		}
	}
	return filtered
}

func (m *Model) clampCommandCursor() {
	commands := m.filteredPaletteCommands()
	if len(commands) == 0 {
		m.commandCursor = 0
		return
	}
	m.commandCursor = min(max(0, m.commandCursor), len(commands)-1)
}

func (m *Model) handleCommandPaletteKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+k":
		m.overlay = overlayNone
		m.setStatus("command palette closed")
	case "up":
		m.commandCursor--
		m.clampCommandCursor()
	case "down":
		m.commandCursor++
		m.clampCommandCursor()
	case "home":
		m.commandCursor = 0
	case "end":
		m.commandCursor = len(m.filteredPaletteCommands()) - 1
		m.clampCommandCursor()
	case "backspace":
		runes := []rune(m.commandQuery)
		if len(runes) > 0 {
			m.commandQuery = string(runes[:len(runes)-1])
			m.commandCursor = 0
		}
	case "ctrl+u":
		m.commandQuery = ""
		m.commandCursor = 0
	case "enter":
		commands := m.filteredPaletteCommands()
		if len(commands) == 0 {
			m.setError("no matching command")
			return nil
		}
		command := commands[m.commandCursor]
		m.overlay = overlayNone
		return m.runPaletteCommand(command.id)
	default:
		if len(msg.Runes) > 0 {
			m.commandQuery += string(msg.Runes)
			m.commandCursor = 0
			m.clampCommandCursor()
		}
	}
	return nil
}

func (m *Model) runPaletteCommand(id commandID) tea.Cmd {
	switch id {
	case commandConnect:
		return m.openServerList()
	case commandDisconnect:
		if m.conn != nil {
			return m.disconnect()
		}
		m.setError("not connected")
	case commandBrowseIdentity:
		cmd := m.openConnectForm()
		m.connectField = connectFieldIdentity
		m.connectCursor = len([]rune(m.connectFieldValue(connectFieldIdentity)))
		m.openConnectIdentityBrowser()
		return cmd
	case commandRefresh:
		return m.refresh()
	case commandToggleHidden:
		return m.toggleHidden()
	case commandTheme:
		m.overlay = overlayTheme
		m.themePicker.Open(m.theme.Name)
	case commandSettings:
		m.overlay = overlaySettings
		m.settingsCursor = 0
	case commandUpload:
		return m.queueUpload()
	case commandDownload:
		return m.queueDownload()
	case commandCancelTransfers:
		m.cancelActiveTransfers()
	case commandResetLayout:
		m.fileSplit = tideui.NewPaneRatio(tideui.PaneRatioOptions{Initial: 0.5, Min: 0.25, Max: 0.75, Step: 0.03})
		m.bottomSplit = tideui.NewPaneRatio(tideui.PaneRatioOptions{Initial: 0.28, Min: 0.15, Max: 0.50, Step: 0.03})
		m.setStatus("layout reset")
		return m.persist()
	case commandHelp:
		m.openHelpOverlay()
	case commandNewFolder:
		m.openMkdirPrompt()
	case commandRename:
		m.openRenamePrompt()
	case commandDelete:
		m.openDeletePrompt()
	case commandEditFile:
		return m.startEdit()
	}
	return nil
}
