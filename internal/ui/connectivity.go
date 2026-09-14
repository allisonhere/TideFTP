package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"tideftp/internal/domain"
	"tideftp/internal/netcheck"
	"tideftp/internal/session"
	"tideftp/internal/transfer"
)

// connectivityFailureThreshold is how many transfers must fail back to back
// before the UI spends a probe on it. One failure is a file; two in a row is
// the shape of a link that has gone away, and the probe itself is cheap and
// harmless on a healthy network.
const connectivityFailureThreshold = 2

// connectivityProbeTimeout bounds the dial the probe makes. Short on purpose:
// it runs while the user is watching a queue stall.
const connectivityProbeTimeout = netcheck.DefaultTimeout

// connectivityRetryDelays is the re-probe backoff once the queue is paused.
// The last entry repeats, so a long outage polls every 30s rather than giving
// up or hammering the link.
var connectivityRetryDelays = []time.Duration{
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	20 * time.Second,
	30 * time.Second,
}

// connectivityPhase is where the probe state machine is.
type connectivityPhase int

const (
	connectivityIdle connectivityPhase = iota
	connectivityChecking
	connectivityPaused
)

// connectivityState is the probe in flight, or the outage being waited out.
type connectivityState struct {
	phase   connectivityPhase
	status  netcheck.Status
	detail  string
	attempt int
	token   int
}

// connectivityResultMsg carries one probe's verdict back to the UI goroutine.
type connectivityResultMsg struct {
	token  int
	result netcheck.Result
}

// connectivityRetryMsg fires when a paused queue is due to re-probe.
type connectivityRetryMsg struct{ token int }

// resetConnectivity abandons any check or outage. It is called whenever the
// user or the connection takes over — a deliberate connect, a drop that
// starts the reconnect campaign, or the check being switched off — so a stale
// pause can never outlive the situation that caused it.
func (m *Model) resetConnectivity() {
	if m.connectivity.phase == connectivityIdle && m.transferFailureStreak == 0 {
		return
	}
	m.connectivity = connectivityState{}
	m.connectivityToken++
	m.transferFailureStreak = 0
}

// noteTransferOutcome records one engine event and, once enough transfers have
// failed in a row, spends a connectivity probe. It returns a command only when
// a probe should run; the caller batches it with the event pump.
func (m *Model) noteTransferOutcome(event transfer.Event) tea.Cmd {
	switch event.Kind {
	case transfer.Completed, transfer.Canceled:
		m.transferFailureStreak = 0
	case transfer.Failed:
		m.transferFailureStreak++
	default:
		return nil
	}
	if !m.checkConnectivity || !m.connected() {
		return nil
	}
	if m.connectivity.phase != connectivityIdle {
		return nil
	}
	if m.transferFailureStreak < connectivityFailureThreshold {
		return nil
	}
	return m.beginConnectivityCheck()
}

// beginConnectivityCheck launches one probe against the connected target. The
// token retires its result if the connection is taken over before it lands.
func (m *Model) beginConnectivityCheck() tea.Cmd {
	host, port := m.connectivityAddress()
	m.connectivityToken++
	token := m.connectivityToken
	m.connectivity = connectivityState{phase: connectivityChecking, token: token}
	return connectivityProbeCmd(token, host, port)
}

// applyConnectivityResult folds a probe in. Reachable clears a pause and lets
// the queue move again; anything else keeps (or starts) the pause and
// schedules another look.
func (m *Model) applyConnectivityResult(msg connectivityResultMsg) tea.Cmd {
	if msg.token != m.connectivityToken {
		return nil
	}
	if msg.result.Status == netcheck.Reachable {
		hadOutage := m.connectivity.status != netcheck.Reachable
		m.connectivity = connectivityState{token: msg.token}
		m.transferFailureStreak = 0
		// Resume whenever the check was holding the queue, whether it was a
		// confirmed pause or the probe that ran first to find out.
		m.startQueuedTransfers()
		if hadOutage {
			m.setStatus("network back — resuming queued transfers")
		}
		return nil
	}
	// Carry the attempt count forward so the backoff actually advances across
	// re-probes rather than restarting at the first delay every time.
	attempt := m.connectivity.attempt
	m.connectivity = connectivityState{
		phase:   connectivityPaused,
		status:  msg.result.Status,
		detail:  msg.result.Detail,
		attempt: attempt,
		token:   msg.token,
	}
	m.setError(m.connectivityMessage())
	return m.scheduleConnectivityRetry()
}

// applyConnectivityRetry re-probes a paused queue on its backoff. The token
// and the phase are both checked so a retry from an abandoned outage is a
// no-op.
func (m *Model) applyConnectivityRetry(msg connectivityRetryMsg) tea.Cmd {
	if msg.token != m.connectivityToken || m.connectivity.phase != connectivityPaused {
		return nil
	}
	host, port := m.connectivityAddress()
	m.connectivity = connectivityState{phase: connectivityChecking, status: m.connectivity.status, detail: m.connectivity.detail, attempt: m.connectivity.attempt, token: msg.token}
	return connectivityProbeCmd(msg.token, host, port)
}

// scheduleConnectivityRetry queues the next re-probe and advances the backoff.
func (m *Model) scheduleConnectivityRetry() tea.Cmd {
	delay := connectivityRetryDelay(m.connectivity.attempt)
	m.connectivity.attempt++
	token := m.connectivityToken
	return tea.Tick(delay, func(time.Time) tea.Msg { return connectivityRetryMsg{token: token} })
}

func connectivityRetryDelay(attempt int) time.Duration {
	if attempt >= len(connectivityRetryDelays) {
		return connectivityRetryDelays[len(connectivityRetryDelays)-1]
	}
	return connectivityRetryDelays[attempt]
}

// connectivityProbeCmd runs the probe off the UI goroutine.
func connectivityProbeCmd(token int, host string, port int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), connectivityProbeTimeout)
		defer cancel()
		result := netcheck.Probe(ctx, host, port, connectivityProbeTimeout)
		return connectivityResultMsg{token: token, result: result}
	}
}

// connectivityAddress is the host:port the probe dials, using the protocol
// default when the target did not name a port.
func (m Model) connectivityAddress() (string, int) {
	port := m.target.Port
	if port == 0 {
		port = session.DefaultPort(m.target.Protocol)
	}
	return m.target.Host, port
}

// connectivityMessage is the status line for a paused queue.
func (m Model) connectivityMessage() string {
	message := "network problem — " + m.connectivityReason() + "; queue paused"
	if held := m.queuedTransferCount(); held > 0 {
		message += fmt.Sprintf(" (%d queued)", held)
	}
	return message
}

// connectivityReason names the failure in terms the user can act on: their own
// network or the server, never a transferred file.
func (m Model) connectivityReason() string {
	if m.connectivity.status == netcheck.NoLink {
		return "no network connection"
	}
	return "server unreachable"
}

func (m Model) queuedTransferCount() int {
	count := 0
	for _, row := range m.transfers {
		if row.Status == domain.Queued {
			count++
		}
	}
	return count
}

// connectivityBannerRows is how many rows the paused-queue banner takes, read
// by both the renderer and the scroll arithmetic. A re-probe while paused is
// still an outage from the user's point of view, so the banner stays up rather
// than flickering off for the length of each check.
func (m Model) connectivityBannerRows() int {
	if m.connectivity.phase == connectivityPaused {
		return 1
	}
	if m.connectivity.phase == connectivityChecking && m.connectivity.status != netcheck.Reachable {
		return 1
	}
	return 0
}

// renderConnectivityBannerRows draws the paused-queue strip. It is pinned
// under the tab bar like the delete and queue meters, so it stays visible
// wherever the user is looking while the queue is held.
func (m Model) renderConnectivityBannerRows(renderer tideui.Renderer, width int) []string {
	if m.connectivityBannerRows() == 0 {
		return nil
	}
	bg, fg := rowSurface(renderer, renderer.Styles.DetailBody)
	accent := readableOn(renderer.Styles.Theme.Error, bg, textMinContrast)
	warning := m.glyph(renderer, "⚠", "!")
	left := segment(bg, accent, fmt.Sprintf(" %s network appears offline — %s", warning, m.connectivityReason()))
	right := segment(bg, renderer.Styles.Theme.Dimmed, fmt.Sprintf("%d queued held ", m.queuedTransferCount()))
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return []string{clampView(left+segment(bg, fg, strings.Repeat(" ", gap))+right, width, 1, bg)}
}
