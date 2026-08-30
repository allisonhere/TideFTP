package ui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/session"
)

// reconnectDelays is the backoff schedule, one entry per attempt. It also
// fixes how many attempts there are: a server that has not come back after
// about a minute of trying is down rather than blinking, and a client that
// redials forever is a client nobody can tell has given up.
var reconnectDelays = []time.Duration{
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	15 * time.Second,
	30 * time.Second,
}

// reconnectState tracks one auto-reconnect campaign: everything needed to
// redial after a connection dropped on its own, plus where the user was when
// it did.
type reconnectState struct {
	target session.Target
	creds  session.Credentials
	// attempt is how many redials have already been made, so
	// reconnectDelays[attempt] is the wait before the next one.
	attempt int
	// resumePath is the remote directory the user was in when the connection
	// dropped. Reconnecting to a server and landing back at the home
	// directory loses the user's place for no reason; a path that no longer
	// lists just falls back to home like any other failed listing.
	resumePath string
	// token invalidates ticks belonging to a campaign that has since been
	// superseded — a user who connects somewhere else by hand must not have
	// a stale timer drag them back to the old server.
	token int
}

// reconnectTickMsg fires when a backoff wait is over.
type reconnectTickMsg struct{ token int }

// beginReconnect starts redialling after an unexpected drop. It is called
// only from applyDisconnected, and only for a drop the user did not ask
// for; a Close the user requested ends the connection and leaves it ended.
func (m *Model) beginReconnect(resumePath string) tea.Cmd {
	if !m.autoReconnect || m.target.Host == "" {
		return nil
	}
	m.reconnectToken++
	m.reconnect = &reconnectState{
		target:     m.target,
		creds:      m.lastCreds,
		resumePath: resumePath,
		token:      m.reconnectToken,
	}
	return m.scheduleReconnect()
}

// cancelReconnect abandons any campaign in progress. Every path where the
// user takes the connection into their own hands — connecting somewhere,
// disconnecting deliberately — goes through this, so a timer set before
// they acted can never fire after it.
func (m *Model) cancelReconnect() {
	if m.reconnect == nil {
		return
	}
	m.reconnect = nil
	m.reconnectToken++
}

// scheduleReconnect waits out the current attempt's backoff. It returns nil
// once the schedule is exhausted, which is what ends a campaign.
func (m *Model) scheduleReconnect() tea.Cmd {
	state := m.reconnect
	if state == nil {
		return nil
	}
	if state.attempt >= len(reconnectDelays) {
		m.reconnect = nil
		m.setError(fmt.Sprintf("gave up reconnecting to %s after %d attempts — press c to retry", state.target.Label(), len(reconnectDelays)))
		return nil
	}
	delay := reconnectDelays[state.attempt]
	token := state.token
	// The first wait still has to report the drop that started all this —
	// "reconnecting" alone never tells the user their connection went away.
	// The whole campaign reads as an error because the whole campaign is
	// time spent disconnected.
	lead := "connection lost"
	if state.attempt > 0 {
		lead = "reconnect failed"
	}
	m.setError(fmt.Sprintf("%s — retrying %s in %s (attempt %d/%d)", lead, state.target.Label(), delay, state.attempt+1, len(reconnectDelays)))
	return tea.Tick(delay, func(time.Time) tea.Msg { return reconnectTickMsg{token: token} })
}

// applyReconnectTick redials, unless the campaign this tick belongs to has
// been superseded or has already succeeded.
func (m *Model) applyReconnectTick(msg reconnectTickMsg) tea.Cmd {
	state := m.reconnect
	if state == nil || state.token != msg.token {
		return nil
	}
	state.attempt++
	m.logs = append(m.logs, fmt.Sprintf("auto-reconnect attempt %d to %s", state.attempt, state.target.Address()))
	// connectFor, not connect: connect cancels the campaign, which is right
	// for a connection the user asked for and wrong for this one.
	return m.connectFor(state.target, state.creds)
}

// reconnectAfterFailure keeps a campaign going when a redial did not land.
// It returns nil — ending the campaign — when there is nothing in flight,
// which is also what makes an ordinary manual connect failure behave
// exactly as it always did.
func (m *Model) reconnectAfterFailure() tea.Cmd {
	if m.reconnect == nil {
		return nil
	}
	return m.scheduleReconnect()
}

// reconnectResumePath is where a freshly (re)connected session should open,
// and whether a campaign asked for somewhere other than the target's home.
// It also retires the campaign: a connection that opened is a campaign that
// succeeded.
func (m *Model) reconnectResumePath() (string, bool) {
	state := m.reconnect
	if state == nil {
		return "", false
	}
	m.reconnect = nil
	m.reconnectToken++
	if state.resumePath == "" {
		return "", false
	}
	return state.resumePath, true
}
