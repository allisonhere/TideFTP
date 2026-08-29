package ui

import (
	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/credstore"
	"tideftp/internal/session"
)

// The server list is the first thing `c` shows: a picker over saved profiles
// plus a trailing "new connection" row. Enter connects to the highlighted
// server straight away (dropping into the connect form only when it needs a
// password that isn't stored); e edits it in the form; n or the last row opens
// a blank form.

// openServerList shows the picker, or the blank connect form when there is
// nothing saved to pick from.
func (m *Model) openServerList() tea.Cmd {
	if len(m.profiles) == 0 {
		return m.openConnectForm()
	}
	m.serverListCursor = min(max(m.serverListCursor, 0), len(m.profiles))
	m.overlay = overlayServerList
	return nil
}

// serverListRows is the number of selectable rows: one per profile, plus the
// "new connection" row at the end.
func (m Model) serverListRows() int { return len(m.profiles) + 1 }

// serverListOnNewRow reports whether the cursor is on the trailing
// "new connection" row rather than a saved profile.
func (m Model) serverListOnNewRow() bool { return m.serverListCursor >= len(m.profiles) }

func (m *Model) handleServerListKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q":
		m.overlay = overlayNone
		m.setStatus("cancelled")
	case "up", "k":
		m.serverListCursor = max(0, m.serverListCursor-1)
	case "down", "j":
		m.serverListCursor = min(m.serverListRows()-1, m.serverListCursor+1)
	case "home":
		m.serverListCursor = 0
	case "end":
		m.serverListCursor = m.serverListRows() - 1
	case "n":
		return m.openConnectForm()
	case "enter":
		if m.serverListOnNewRow() {
			return m.openConnectForm()
		}
		return m.connectToServer(m.serverListCursor)
	case "e":
		if m.serverListOnNewRow() {
			return m.openConnectForm()
		}
		return m.openConnectFormFor(m.profiles[m.serverListCursor], connectFieldName)
	case "d":
		if m.serverListOnNewRow() {
			return nil
		}
		cmd := m.deleteProfileAt(m.serverListCursor)
		if len(m.profiles) == 0 {
			return tea.Batch(cmd, m.openConnectForm())
		}
		m.serverListCursor = min(m.serverListCursor, len(m.profiles)-1)
		return cmd
	}
	return nil
}

// connectToServer dials profile i. An SFTP profile authenticates with the
// agent or a key file, so it can connect with no interaction. FTP/FTPS always
// need a password: it is looked up in the keyring, and if it isn't there the
// connect form opens on the Password field instead.
func (m *Model) connectToServer(i int) tea.Cmd {
	target := m.profiles[i]
	if target.Protocol == "sftp" {
		m.overlay = overlayNone
		return m.connect(target, session.Credentials{})
	}
	if m.creds == nil {
		return m.openConnectFormFor(target, connectFieldPassword)
	}
	return serverConnectLookupCmd(m.creds, target)
}

// serverConnectMsg carries the result of the keyring lookup a direct connect
// to a password profile kicks off.
type serverConnectMsg struct {
	target   session.Target
	password string
	found    bool
}

func serverConnectLookupCmd(store credstore.Store, target session.Target) tea.Cmd {
	key := credentialKey(target)
	return func() tea.Msg {
		password, ok, err := store.Get(key)
		return serverConnectMsg{target: target, password: password, found: ok && err == nil}
	}
}
