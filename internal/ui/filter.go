package ui

import (
	"fmt"
	"path"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
)

// filterGlobMeta are the characters that make a filter query a glob rather
// than a plain substring match.
const filterGlobMeta = "*?["

// entryMatchesFilter reports whether name passes query. An empty query
// matches everything, and the parent entry ".." always passes so a filtered
// listing can still be walked out of. A query containing glob
// metacharacters is matched with path.Match (falling back to substring if
// it is not a valid pattern); anything else is a case-insensitive substring
// test.
func entryMatchesFilter(name, query string) bool {
	query = strings.TrimSpace(query)
	if query == "" || name == parentEntryName {
		return true
	}
	name, query = strings.ToLower(name), strings.ToLower(query)
	if strings.ContainsAny(query, filterGlobMeta) {
		if ok, err := path.Match(query, name); err == nil {
			return ok
		}
	}
	return strings.Contains(name, query)
}

// applyFilter recomputes entries from allEntries against the current filter
// query, then clamps the cursor and scroll offset back into range. It is a
// no-op copy when the query is empty.
func (p *filePane) applyFilter(visible int) {
	if p.allEntries == nil {
		p.allEntries = p.entries
	}
	if strings.TrimSpace(p.filter) == "" {
		p.entries = p.allEntries
		p.clamp(visible)
		return
	}
	filtered := make([]domain.Entry, 0, len(p.allEntries))
	for _, entry := range p.allEntries {
		if entryMatchesFilter(entry.Name, p.filter) {
			filtered = append(filtered, entry)
		}
	}
	p.entries = filtered
	p.clamp(visible)
}

// clearFilter drops the query and the input, restoring the full listing.
func (p *filePane) clearFilter(visible int) {
	p.filter, p.filtering = "", false
	if p.allEntries != nil {
		p.entries = p.allEntries
	}
	p.clamp(visible)
}

// filterActive reports whether the pane is showing a filtered listing or is
// in the middle of typing a query.
func (p filePane) filterActive() bool {
	return p.filtering || strings.TrimSpace(p.filter) != ""
}

// filterCounts is how many real entries the filter shows out of how many
// the directory holds, both excluding the "..' parent row.
func (p filePane) filterCounts() (shown, total int) {
	shown, total = len(p.entries), len(p.allEntries)
	if total == 0 {
		total = shown
	}
	for _, entry := range p.entries {
		if isParentDirEntry(entry) {
			shown--
			break
		}
	}
	for _, entry := range p.allEntries {
		if isParentDirEntry(entry) {
			total--
			break
		}
	}
	return shown, total
}

// handleFilterKey feeds keys to the focused pane's live filter input while
// it is open. Typing edits the query and the listing narrows on every
// keystroke; esc cancels and restores the full listing; enter, tab, and
// shift+tab accept it (leaving it applied) and resume normal keys; the
// arrow keys still move the cursor through the narrowed rows so a match can
// be acted on straight away.
func (m *Model) handleFilterKey(msg tea.KeyMsg) tea.Cmd {
	pane := m.focusedFilePane()
	if pane == nil {
		return nil
	}
	visible := m.filePaneVisibleRows()
	switch msg.String() {
	case "ctrl+c":
		if m.conn != nil {
			return tea.Sequence(closeConnCmd(m.conn), tea.Quit)
		}
		return tea.Quit
	case "esc":
		pane.clearFilter(visible)
		m.setStatus("filter cleared")
	case "enter":
		if strings.TrimSpace(pane.filter) == "" {
			pane.clearFilter(visible)
			m.setStatus("filter cleared")
			return nil
		}
		pane.filtering = false
		shown, total := pane.filterCounts()
		m.setStatus(fmt.Sprintf("filter %q — %d of %d", strings.TrimSpace(pane.filter), shown, total))
	case "tab":
		pane.filtering = false
		m.focus = (m.focus + 1) % 3
	case "shift+tab":
		pane.filtering = false
		m.focus = (m.focus + 2) % 3
	case "up", "ctrl+p":
		pane.cursor--
		pane.clamp(visible)
	case "down", "ctrl+n":
		pane.cursor++
		pane.clamp(visible)
	case "pgup":
		pane.cursor -= visible
		pane.clamp(visible)
	case "pgdown":
		pane.cursor += visible
		pane.clamp(visible)
	case "backspace":
		runes := []rune(pane.filter)
		if len(runes) > 0 {
			pane.filter = string(runes[:len(runes)-1])
			pane.applyFilter(visible)
		}
	case "ctrl+u":
		pane.filter = ""
		pane.applyFilter(visible)
	default:
		if len(msg.Runes) > 0 {
			pane.filter += string(msg.Runes)
			pane.applyFilter(visible)
		}
	}
	return nil
}
