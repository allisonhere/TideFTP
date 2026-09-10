package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Bookmarks are the directories worth coming back to. A profile's StartPath
// already covers one of them, but only one, and only by reconnecting; these
// are the rest, reachable mid-session with two keys.
//
// Remote bookmarks belong to the saved profile they were taken on rather than
// to the app: `/var/www` means something on one host and nothing — or worse,
// something else — on another. Local bookmarks are global for the mirror-image
// reason: the local pane is not a property of whichever server you dialled.
//
// A bookmark is just a path. The path is its own label, so there is no name to
// prompt for, edit, or keep in step with a directory that moved.

// bookmarkDeleteWindow is how long a first `d` in the picker stays armed. It
// matches the server list's window; see serverDeleteWindow for the reasoning.
const bookmarkDeleteWindow = 5 * time.Second

// bookmarksFor returns pane's bookmarks and whether that list can be written
// to.
//
// The local list always can. The remote list lives on the saved profile for
// the current connection, so a connection with no saved profile — one dialled
// from the command line — has nowhere to put a bookmark. It reports that
// rather than inventing a profile: `B` says it bookmarks a directory, and
// silently creating a saved server the user would then find in the `c` list is
// not what it said it would do.
func (m Model) bookmarksFor(pane paneID) (paths []string, writable bool) {
	if pane == paneLocal {
		return m.localBookmarks, true
	}
	idx := m.profileIndexOf(m.target)
	if idx < 0 {
		return nil, false
	}
	return m.profiles[idx].Bookmarks, true
}

// setBookmarksFor writes pane's bookmarks back to whichever list owns them.
// It assumes bookmarksFor reported the pane writable.
func (m *Model) setBookmarksFor(pane paneID, paths []string) {
	if pane == paneLocal {
		m.localBookmarks = paths
		return
	}
	if idx := m.profileIndexOf(m.target); idx >= 0 {
		m.profiles[idx].Bookmarks = paths
	}
}

// bookmarkPath is the directory `B` would act on: the pane's committed path,
// not displayPath, which returns pendingPath while a listing is in flight and
// would bookmark a directory that may yet fail to open.
func (m *Model) bookmarkPath(pane paneID) string {
	return m.filePaneByID(pane).path
}

// toggleBookmark adds the focused pane's directory, or removes it if it is
// already bookmarked. One key for both directions: the common case is a
// directory you want on the list or off it, and having `B` mean "add" only
// would leave the picker's `dd` as the sole way to undo a mistaken press.
func (m *Model) toggleBookmark() tea.Cmd {
	pane, ok := m.focusedPaneID()
	if !ok {
		m.setError("focus a file pane to bookmark a directory")
		return nil
	}
	return m.toggleBookmarkFor(pane)
}

// toggleBookmarkFor is toggleBookmark against an explicit pane, so the open
// picker can add the current directory to the list it is already showing
// rather than to whichever pane happens to hold focus.
func (m *Model) toggleBookmarkFor(pane paneID) tea.Cmd {
	paths, writable := m.bookmarksFor(pane)
	if !writable {
		// Two different reasons a remote bookmark has nowhere to go, and
		// telling the user to save a server they are not even connected to
		// would send them the wrong way.
		if pane == paneRemote && !m.connected() {
			m.setError("not connected")
		} else {
			m.setError("save this server to bookmark its directories")
		}
		return nil
	}
	path := m.bookmarkPath(pane)
	if path == "" {
		m.setError("nothing to bookmark yet")
		return nil
	}
	for i, existing := range paths {
		if existing == path {
			m.setBookmarksFor(pane, append(append([]string(nil), paths[:i]...), paths[i+1:]...))
			// Only meaningful while the picker is open; bookmarkPane is stale
			// otherwise and the cursor is reset on the next open anyway.
			if m.overlay == overlayBookmarks {
				m.clampBookmarkCursor()
			}
			m.setStatus("removed bookmark " + path)
			return m.persist()
		}
	}
	m.setBookmarksFor(pane, append(append([]string(nil), paths...), path))
	m.setStatus("bookmarked " + path)
	return m.persist()
}

// openBookmarks shows the picker for the focused pane.
//
// The pane is captured now rather than read on each keypress: the overlay
// swallows every key, but nothing else guarantees focus stays put, and a
// picker that silently retargeted mid-use could jump the wrong pane.
//
// An empty list still opens. Refusing would be a dead end — the overlay is
// where the user finds out `B` is what fills it.
func (m *Model) openBookmarks() tea.Cmd {
	pane, ok := m.focusedPaneID()
	if !ok {
		m.setError("focus a file pane to open bookmarks")
		return nil
	}
	m.bookmarkPane = pane
	m.bookmarkCursor = 0
	m.disarmBookmarkDelete()
	m.overlay = overlayBookmarks
	return nil
}

func (m *Model) handleBookmarksKey(msg tea.KeyMsg) tea.Cmd {
	paths, writable := m.bookmarksFor(m.bookmarkPane)
	switch msg.String() {
	case "esc", "q", "b":
		m.disarmBookmarkDelete()
		m.overlay = overlayNone
		m.setStatus("cancelled")
	case "up", "k":
		m.disarmBookmarkDelete()
		m.bookmarkCursor = max(0, m.bookmarkCursor-1)
	case "down", "j":
		m.disarmBookmarkDelete()
		m.bookmarkCursor = min(len(paths)-1, m.bookmarkCursor+1)
	case "home":
		m.disarmBookmarkDelete()
		m.bookmarkCursor = 0
	case "end":
		m.disarmBookmarkDelete()
		m.bookmarkCursor = max(0, len(paths)-1)
	case "B":
		// Add or remove the pane's current directory without leaving the
		// picker, so building a list is one key repeated rather than a
		// close-navigate-reopen cycle.
		m.disarmBookmarkDelete()
		cmd := m.toggleBookmarkFor(m.bookmarkPane)
		m.clampBookmarkCursor()
		return cmd
	case "enter":
		if m.bookmarkCursor < 0 || m.bookmarkCursor >= len(paths) {
			return nil
		}
		path := paths[m.bookmarkCursor]
		m.disarmBookmarkDelete()
		m.overlay = overlayNone
		// navigateTo refuses to commit a path whose listing fails, so a
		// bookmark to a directory that has since gone leaves the pane where
		// it was and reports the error itself.
		return m.navigateTo(m.bookmarkPane, path)
	case "d":
		if !writable || m.bookmarkCursor < 0 || m.bookmarkCursor >= len(paths) {
			return nil
		}
		if !m.bookmarkArmedFor(m.bookmarkCursor) {
			m.bookmarkDeleteIndex = m.bookmarkCursor
			m.bookmarkDeleteExpiry = time.Now().Add(bookmarkDeleteWindow)
			m.setStatus("press d again to remove " + paths[m.bookmarkCursor])
			return nil
		}
		m.disarmBookmarkDelete()
		return m.deleteBookmarkAt(m.bookmarkCursor)
	}
	return nil
}

// deleteBookmarkAt removes one bookmark and persists the change.
func (m *Model) deleteBookmarkAt(idx int) tea.Cmd {
	paths, writable := m.bookmarksFor(m.bookmarkPane)
	if !writable || idx < 0 || idx >= len(paths) {
		return nil
	}
	removed := paths[idx]
	m.setBookmarksFor(m.bookmarkPane, append(append([]string(nil), paths[:idx]...), paths[idx+1:]...))
	m.clampBookmarkCursor()
	m.setStatus("removed bookmark " + removed)
	return m.persist()
}

// bookmarkArmedFor reports whether a first `d` armed exactly this row and has
// not yet lapsed. As in the server list, the index is part of the state: an
// arming must not follow the cursor onto a row the user never armed.
func (m Model) bookmarkArmedFor(index int) bool {
	return m.bookmarkDeleteIndex == index && time.Now().Before(m.bookmarkDeleteExpiry)
}

func (m *Model) disarmBookmarkDelete() {
	m.bookmarkDeleteIndex = -1
	m.bookmarkDeleteExpiry = time.Time{}
}

// clampBookmarkCursor keeps the cursor on a real row after a removal.
func (m *Model) clampBookmarkCursor() {
	paths, _ := m.bookmarksFor(m.bookmarkPane)
	m.bookmarkCursor = min(max(m.bookmarkCursor, 0), max(len(paths)-1, 0))
}
