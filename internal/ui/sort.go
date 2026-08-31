package ui

import (
	"cmp"
	"fmt"
	"sort"
	"strings"

	"tideftp/internal/domain"
)

// sortKey is which column a file pane orders its listing by.
type sortKey int

const (
	sortByName sortKey = iota
	sortBySize
	sortByModified
	sortByKind
	sortKeyCount
)

func (k sortKey) String() string {
	switch k {
	case sortBySize:
		return "size"
	case sortByModified:
		return "date"
	case sortByKind:
		return "type"
	default:
		return "name"
	}
}

// parseSortKey maps a persisted config value onto a sortKey, defaulting to
// name for anything unrecognised.
func parseSortKey(value string) sortKey {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "size":
		return sortBySize
	case "date", "modified", "mtime":
		return sortByModified
	case "type", "kind":
		return sortByKind
	default:
		return sortByName
	}
}

// sortEntries orders entries in place by key. Direction only reorders within
// a group: ".." is always first, and — for every key except "type", which
// orders by kind explicitly — directories always sort ahead of files. Ties
// break on the lower-cased name so the order is stable and deterministic.
func sortEntries(entries []domain.Entry, key sortKey, desc bool) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if ap, bp := isParentDirEntry(a), isParentDirEntry(b); ap || bp {
			return ap && !bp
		}
		if key != sortByKind && a.IsDir() != b.IsDir() {
			return a.IsDir()
		}
		c := compareEntries(a, b, key)
		if c == 0 {
			c = cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		}
		if desc {
			c = -c
		}
		return c < 0
	})
}

// compareEntries is the by-key comparison, before the name tiebreak and the
// direction flip that sortEntries applies. A zero result means "equal by
// this key" and defers entirely to the tiebreak.
func compareEntries(a, b domain.Entry, key sortKey) int {
	switch key {
	case sortBySize:
		return cmp.Compare(a.Size, b.Size)
	case sortByModified:
		return a.Modified.Compare(b.Modified)
	case sortByKind:
		return cmp.Compare(int(a.Kind), int(b.Kind))
	default: // sortByName — the tiebreak already is the name comparison
		return 0
	}
}

// resort re-orders the pane's held listing by its current sort key and
// direction, reapplies any active filter, and keeps the cursor on whatever
// entry it was pointing at.
func (p *filePane) resort(visible int) {
	var under string
	if entry, ok := p.current(); ok {
		under = entry.Name
	}
	if p.allEntries == nil {
		p.allEntries = p.entries
	}
	sortEntries(p.allEntries, p.sortKey, p.sortDesc)
	p.applyFilter(visible)
	if under != "" {
		for i, entry := range p.entries {
			if entry.Name == under {
				p.cursor = i
				break
			}
		}
		p.clamp(visible)
	}
}

// sortMarker is the compact sort indicator shown in a pane's title, e.g.
// "↓size". It is empty for the default (name, ascending) so the common case
// leaves the header uncluttered.
func (m Model) sortMarker(pane filePane) string {
	if pane.sortKey == sortByName && !pane.sortDesc {
		return ""
	}
	arrow := m.glyphPlain("↑", "^")
	if pane.sortDesc {
		arrow = m.glyphPlain("↓", "v")
	}
	return arrow + pane.sortKey.String()
}

// cycleSortKey advances the focused pane to the next sort column, wrapping.
func (m *Model) cycleSortKey() {
	pane := m.focusedFilePane()
	if pane == nil {
		m.setStatus("sort: focus a file pane first")
		return
	}
	pane.sortKey = (pane.sortKey + 1) % sortKeyCount
	pane.resort(m.filePaneVisibleRows())
	m.setStatus(fmt.Sprintf("%s sorted by %s, %s", paneLabel(m.focus), pane.sortKey, sortDirLabel(pane.sortDesc)))
}

// toggleSortDir flips the focused pane between ascending and descending.
func (m *Model) toggleSortDir() {
	pane := m.focusedFilePane()
	if pane == nil {
		m.setStatus("sort: focus a file pane first")
		return
	}
	pane.sortDesc = !pane.sortDesc
	pane.resort(m.filePaneVisibleRows())
	m.setStatus(fmt.Sprintf("%s sorted by %s, %s", paneLabel(m.focus), pane.sortKey, sortDirLabel(pane.sortDesc)))
}

func sortDirLabel(desc bool) string {
	if desc {
		return "descending"
	}
	return "ascending"
}

func paneLabel(focus focusPane) string {
	if focus == focusRemote {
		return "remote"
	}
	return "local"
}

// sortDefaultPane is the pane whose sort order snapshotConfig persists as the
// startup default: the focused file pane when there is one, so re-sorting a
// pane and quitting keeps that order, otherwise the local pane.
func (m Model) sortDefaultPane() filePane {
	if m.focus == focusRemote {
		return m.remote
	}
	return m.local
}
