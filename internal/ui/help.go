package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type helpEntry struct {
	section string
	key     string
	label   string
}

var helpEntries = []helpEntry{
	{section: "Navigate", key: "tab / shift+tab", label: "switch panes"},
	{section: "Navigate", key: "up/down, k/j", label: "move cursor"},
	{section: "Navigate", key: "pgup / pgdown", label: "page up / down"},
	{section: "Navigate", key: "enter", label: "open directory"},
	{section: "Navigate", key: "backspace / h", label: "parent directory"},
	{section: "Act", key: "space", label: "toggle selection"},
	{section: "Act", key: "ctrl+a", label: "select all"},
	{section: "Act", key: "esc", label: "clear selection / cancel"},
	{section: "Act", key: "u", label: "upload"},
	{section: "Act", key: "d", label: "download"},
	{section: "Act", key: "r", label: "refresh"},
	{section: "Act", key: "n", label: "new folder"},
	{section: "Act", key: "f2", label: "rename item"},
	{section: "Act", key: "e", label: "edit file in your editor"},
	{section: "Act", key: "v", label: "preview file (text or hex)"},
	{section: "Act", key: "y", label: "copy path(s) to clipboard"},
	{section: "Act", key: "delete", label: "delete selected / highlighted"},
	{section: "Act", key: "x", label: "cancel active transfer (queue pane) / all"},
	{section: "Act", key: "R", label: "retry selected failed transfer"},
	{section: "Act", key: "+/-", label: "more/fewer parallel transfers"},
	{section: "Act", key: ".", label: "toggle hidden files"},
	{section: "View", key: "c", label: "connect / disconnect"},
	{section: "View", key: "ctrl+k", label: "command palette"},
	{section: "View", key: "t", label: "theme picker"},
	{section: "View", key: ",", label: "settings"},
	{section: "View", key: "i", label: "toggle icons"},
	{section: "View", key: "shift+left/right", label: "resize file panes"},
	{section: "View", key: "shift+up/down", label: "resize transfer pane"},
	{section: "View", key: "ctrl+0", label: "reset layout"},
	{section: "View", key: "1-6", label: "bottom tabs"},
	{section: "View", key: "q / ctrl+c", label: "quit"},
}

func (m *Model) openHelpOverlay() {
	m.helpQuery = ""
	m.helpOffset = 0
	m.overlay = overlayHelp
}

func (m Model) filteredHelpEntries() []helpEntry {
	query := strings.ToLower(strings.TrimSpace(m.helpQuery))
	if query == "" {
		return helpEntries
	}
	filtered := make([]helpEntry, 0, len(helpEntries))
	for _, entry := range helpEntries {
		haystack := strings.ToLower(entry.section + " " + entry.key + " " + entry.label)
		if strings.Contains(haystack, query) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func (m *Model) clampHelpOffset() {
	visible := m.helpVisibleRows()
	total := len(helpDisplayRows(m.filteredHelpEntries()))
	m.helpOffset = min(max(0, m.helpOffset), max(0, total-visible))
}

func (m Model) helpVisibleRows() int {
	return max(5, min(18, m.height-12))
}

func helpDisplayRows(entries []helpEntry) []helpEntry {
	if len(entries) == 0 {
		return nil
	}
	rows := make([]helpEntry, 0, len(entries)+3)
	lastSection := ""
	for _, entry := range entries {
		if entry.section != lastSection {
			if len(rows) > 0 {
				rows = append(rows, helpEntry{})
			}
			rows = append(rows, helpEntry{section: entry.section})
			lastSection = entry.section
		}
		rows = append(rows, entry)
	}
	return rows
}

func (m *Model) handleHelpKey(msg tea.KeyMsg) tea.Cmd {
	// The help overlay is a live search field, so letters must reach the query
	// (searching "quit" or "ctrl+k" was impossible while q/k/j were bound).
	// Only esc closes it; arrows and page keys scroll.
	switch msg.String() {
	case "esc":
		m.overlay = overlayNone
		m.setStatus("help closed")
	case "up":
		m.helpOffset--
		m.clampHelpOffset()
	case "down":
		m.helpOffset++
		m.clampHelpOffset()
	case "pgup":
		m.helpOffset -= m.helpVisibleRows()
		m.clampHelpOffset()
	case "pgdown":
		m.helpOffset += m.helpVisibleRows()
		m.clampHelpOffset()
	case "home":
		m.helpOffset = 0
	case "end":
		m.helpOffset = len(helpDisplayRows(m.filteredHelpEntries()))
		m.clampHelpOffset()
	case "backspace":
		runes := []rune(m.helpQuery)
		if len(runes) > 0 {
			m.helpQuery = string(runes[:len(runes)-1])
			m.helpOffset = 0
		}
	case "ctrl+u":
		m.helpQuery = ""
		m.helpOffset = 0
	default:
		if len(msg.Runes) > 0 {
			m.helpQuery += string(msg.Runes)
			m.helpOffset = 0
			m.clampHelpOffset()
		}
	}
	return nil
}
