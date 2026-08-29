package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/vfs"
)

func fileActionLabel(kind fileActionKind) string {
	switch kind {
	case fileActionMkdir:
		return "new folder"
	case fileActionRename:
		return "rename"
	case fileActionDelete:
		return "delete"
	default:
		return "file action"
	}
}

func fileActionDoneLabel(kind fileActionKind) string {
	switch kind {
	case fileActionMkdir:
		return "folder created"
	case fileActionRename:
		return "renamed"
	case fileActionDelete:
		return "deleted"
	default:
		return "done"
	}
}

func (m *Model) focusedMutablePane() (paneID, *filePane, bool) {
	paneID, ok := m.focusedPaneID()
	if !ok {
		m.setError("focus a file pane first")
		return paneLocal, nil, false
	}
	if paneID == paneRemote && !m.connected() {
		m.setError("not connected")
		return paneLocal, nil, false
	}
	return paneID, m.filePaneByID(paneID), true
}

func (m *Model) openMkdirPrompt() {
	paneID, _, ok := m.focusedMutablePane()
	if !ok {
		return
	}
	m.fileAction = &fileActionPrompt{kind: fileActionMkdir, pane: paneID}
	m.overlay = overlayFileAction
	m.setStatus("new folder")
}

func (m *Model) openRenamePrompt() {
	paneID, pane, ok := m.focusedMutablePane()
	if !ok {
		return
	}
	entry, found := pane.current()
	if !found || isParentDirEntry(entry) {
		m.setError("highlight an item to rename")
		return
	}
	m.fileAction = &fileActionPrompt{kind: fileActionRename, pane: paneID, text: entry.Name, cursor: len([]rune(entry.Name)), oldName: entry.Name}
	m.overlay = overlayFileAction
	m.setStatus("rename " + entry.Name)
}

func (m *Model) openDeletePrompt() {
	paneID, pane, ok := m.focusedMutablePane()
	if !ok {
		return
	}
	entries := pane.actionEntries()
	if len(entries) == 0 {
		m.setError("highlight or select item(s) to delete")
		return
	}
	m.fileAction = &fileActionPrompt{kind: fileActionDelete, pane: paneID, entries: entries}
	m.overlay = overlayFileAction
	m.setStatus(fmt.Sprintf("delete %d item(s)?", len(entries)))
}

func (m *Model) handleFileActionKey(msg tea.KeyMsg) tea.Cmd {
	if m.fileAction == nil {
		m.overlay = overlayNone
		return nil
	}
	prompt := m.fileAction

	// The delete prompt is a yes/no confirmation, so single letters are
	// shortcuts. The mkdir/rename prompts are text fields: only esc and enter
	// are reserved, everything else edits the name (so a folder called
	// "notes" or "query" can actually be typed).
	if prompt.kind == fileActionDelete {
		switch msg.String() {
		case "esc", "q", "n":
			m.fileAction = nil
			m.overlay = overlayNone
			m.setStatus("cancelled")
		case "enter", "y":
			return m.submitFileAction()
		}
		return nil
	}

	switch msg.String() {
	case "esc":
		m.fileAction = nil
		m.overlay = overlayNone
		m.setStatus("cancelled")
	case "enter":
		return m.submitFileAction()
	case "backspace":
		runes := []rune(prompt.text)
		if prompt.cursor > 0 && prompt.cursor <= len(runes) {
			prompt.text = string(append(runes[:prompt.cursor-1], runes[prompt.cursor:]...))
			prompt.cursor--
		}
	case "delete":
		runes := []rune(prompt.text)
		if prompt.cursor >= 0 && prompt.cursor < len(runes) {
			prompt.text = string(append(runes[:prompt.cursor], runes[prompt.cursor+1:]...))
		}
	case "left":
		prompt.cursor = max(0, prompt.cursor-1)
	case "right":
		prompt.cursor = min(len([]rune(prompt.text)), prompt.cursor+1)
	case "home":
		prompt.cursor = 0
	case "end":
		prompt.cursor = len([]rune(prompt.text))
	case "ctrl+u":
		prompt.text = ""
		prompt.cursor = 0
	default:
		if len(msg.Runes) > 0 {
			runes := []rune(prompt.text)
			cur := min(max(prompt.cursor, 0), len(runes))
			inserted := msg.Runes
			runes = append(runes[:cur], append(inserted, runes[cur:]...)...)
			prompt.text = string(runes)
			prompt.cursor = cur + len(inserted)
		}
	}
	return nil
}

func (m *Model) submitFileAction() tea.Cmd {
	prompt := m.fileAction
	if prompt == nil {
		return nil
	}
	switch prompt.kind {
	case fileActionMkdir, fileActionRename:
		name := strings.TrimSpace(prompt.text)
		if name == "" {
			m.setError(fileActionLabel(prompt.kind) + " name is required")
			return nil
		}
		if !validFileActionName(name) {
			m.setError("name cannot be \".\", \"..\", or contain a path separator")
			return nil
		}
		prompt.text = name
	}
	m.fileAction = nil
	m.overlay = overlayNone
	m.setStatus(fileActionLabel(prompt.kind) + "...")
	return fileActionCmd(m.fsByID(prompt.pane), m.filePaneByID(prompt.pane).path, *prompt)
}

// validFileActionName rejects names that would resolve outside the current
// directory. mkdir/rename create a single child of the focused pane's path, so
// separators and dot-references are never legitimate here.
func validFileActionName(name string) bool {
	if name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, `/\`+"\x00")
}

func fileActionCmd(fs vfs.FS, base string, prompt fileActionPrompt) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
		defer cancel()
		var err error
		switch prompt.kind {
		case fileActionMkdir:
			err = fs.Mkdir(ctx, fs.Child(base, strings.TrimSpace(prompt.text)))
		case fileActionRename:
			err = fs.Rename(ctx, fs.Child(base, prompt.oldName), fs.Child(base, strings.TrimSpace(prompt.text)))
		case fileActionDelete:
			for _, entry := range prompt.entries {
				if isParentDirEntry(entry) {
					continue
				}
				if err = fs.Remove(ctx, fs.Child(base, entry.Name)); err != nil {
					break
				}
			}
		}
		return fileActionMsg{kind: prompt.kind, pane: prompt.pane, err: err}
	}
}
