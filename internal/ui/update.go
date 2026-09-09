package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"tideftp/internal/update"
)

// updateState is where an update has got to. It is one dimension rather than
// a set of booleans because the states are genuinely exclusive — a transfer
// cannot be downloading and failed at once — and the overlay reads a single
// value to decide what to draw and what enter means.
type updateState int

const (
	// updateIdle covers both "not looked yet" and "looked, nothing newer".
	// updateProgress.checked tells those two apart for the Settings row;
	// nothing else needs to.
	updateIdle updateState = iota
	updateChecking
	updateAvailable
	updateDownloading
	updateInstalling
	updateInstalled
	// updateNeedsElevation means the new binary is downloaded and verified
	// but the install target is not writable, so finishing the job needs a
	// command run outside the app.
	updateNeedsElevation
	updateFailed
)

// updateProgress is everything the UI knows about updating right now.
type updateProgress struct {
	state updateState
	// checked distinguishes "no update found" from "never looked", which is
	// the difference between "up to date" and "not checked yet" in Settings.
	checked bool
	latest  update.ReleaseInfo
	asset   update.DownloadedAsset
	err     error
	// manualCommand is set when the install could not be done in place; it
	// is the command the user has to run themselves.
	manualCommand string
	// removeCommand clears a stale copy of the binary that sits earlier on
	// PATH and would keep shadowing the one just installed.
	removeCommand string
	// percent drives the overlay's progress bar. It is cosmetic: the real
	// work is a single blocking download and install with no byte-level
	// reporting to hook into, so it advances on a timer to show the app is
	// alive rather than to measure anything.
	percent int
	// startedAt is when the install began, and is what percent is derived
	// from — see updateMinDuration.
	startedAt time.Time
	// pending holds an install result that arrived before the bar finished.
	// A download over a fast link can complete in a few hundred milliseconds,
	// and a progress bar that flashes to 30% and vanishes reads as a glitch
	// rather than as work done. Holding the result here until the bar has run
	// its course is what makes a fast update look like a fast update.
	pending *updateInstalledMsg
}

// available reports whether a newer release is waiting and the user has not
// asked to be left alone about this particular one.
func (m Model) updateAvailable() bool {
	return m.update.state == updateAvailable &&
		m.update.latest.Version != "" &&
		m.update.latest.Version != m.updates.DismissedVersion
}

// updateNoticeText is the topbar segment, empty when there is nothing to say.
func (m Model) updateNoticeText() string {
	if !m.updateAvailable() {
		return ""
	}
	return fmt.Sprintf("update %s  U", m.update.latest.Version)
}

// updateCheckedMsg reports a finished check, successful or not.
type updateCheckedMsg struct {
	result update.CheckResult
	err    error
	// manual is set for a check the user asked for, which reports "you are up
	// to date" rather than staying silent the way the startup one does.
	manual bool
}

// updateDownloadedMsg reports a downloaded, checksum-verified archive.
type updateDownloadedMsg struct {
	asset update.DownloadedAsset
	err   error
}

// updateInstalledMsg reports the new binary being in place, or why it is not.
type updateInstalledMsg struct {
	result update.InstallResult
	err    error
}

// updateTickMsg advances the cosmetic progress bar.
type updateTickMsg struct{}

const (
	// updateProgressInterval is how often the bar redraws. 120ms is fast
	// enough to read as motion and slow enough not to churn the screen.
	updateProgressInterval = 120 * time.Millisecond
	// updateMinDuration is how long the bar takes to cross, and so the
	// shortest an update can appear to take. Real installs are often quicker
	// than this; finishing in 200ms would show a bar that jumps to a third
	// and disappears, which looks broken rather than fast.
	updateMinDuration = 3 * time.Second
)

func updateTickCmd() tea.Cmd {
	return tea.Tick(updateProgressInterval, func(time.Time) tea.Msg { return updateTickMsg{} })
}

// maybeCheckForUpdatesCmd is the startup check. It is skipped for a build
// whose version is not a released one — a `go run` build, or anything else
// reporting "dev". That is not just a nicety: IsNewerVersion refuses to
// compare an unparseable version, so such a build could never act on the
// answer, and asking GitHub for one would be a request made purely to throw
// away. It also keeps the test suite off the network, since tests construct
// models with an empty version.
func (m Model) maybeCheckForUpdatesCmd() tea.Cmd {
	if !m.updates.CheckOnStartup || !update.IsStableVersion(m.version) {
		return nil
	}
	return m.checkForUpdatesCmd(false)
}

// checkForUpdatesCmd asks GitHub what the latest release is. manual marks a
// check the user asked for, which reports its result either way.
func (m Model) checkForUpdatesCmd(manual bool) tea.Cmd {
	updater, version := m.updater, m.version
	return func() tea.Msg {
		result, err := updater.Check(version)
		return updateCheckedMsg{result: result, err: err, manual: manual}
	}
}

// applyUpdateChecked folds a finished check into the model. A failed startup
// check is deliberately quiet — the app works fine without knowing, and an
// error banner for a network blip the user did not ask about is noise. A
// failed manual check does report, because someone is waiting on the answer.
func (m *Model) applyUpdateChecked(msg updateCheckedMsg) tea.Cmd {
	m.updates.LastCheckedUnix = time.Now().Unix()
	m.update.checked = true
	m.update.percent = 0

	if msg.err != nil {
		m.update.state = updateFailed
		m.update.err = msg.err
		if msg.manual {
			m.setError(fmt.Sprintf("update check: %v", msg.err))
		}
		return m.persist()
	}

	m.update.err = nil
	if !msg.result.Available {
		m.update.state = updateIdle
		m.update.latest = update.ReleaseInfo{}
		// Nothing newer exists, so a version dismissed earlier is moot —
		// clearing it means a future release is not accidentally suppressed
		// by a stale entry.
		m.updates.DismissedVersion = ""
		if msg.manual {
			m.setStatus("TideFTP is up to date (" + m.version + ")")
		}
		return m.persist()
	}

	m.update.state = updateAvailable
	m.update.latest = msg.result.Latest
	if msg.manual {
		m.setStatus("TideFTP " + msg.result.Latest.Version + " is available — press U to install")
	}
	return m.persist()
}

// startUpdateInstall begins the download. The overlay stays open throughout,
// moving through downloading and installing to one of installed, needs
// elevation, or failed.
func (m *Model) startUpdateInstall() tea.Cmd {
	if m.update.latest.DownloadURL == "" {
		// Nothing to install from — most likely the overlay was opened from a
		// stale state. Re-check rather than fail.
		m.update.state = updateChecking
		return m.checkForUpdatesCmd(true)
	}
	m.update.state = updateDownloading
	m.update.percent = 0
	m.update.startedAt = time.Now()
	m.update.pending = nil
	updater, release := m.updater, m.update.latest
	return tea.Batch(
		updateTickCmd(),
		func() tea.Msg {
			asset, err := updater.Download(release)
			return updateDownloadedMsg{asset: asset, err: err}
		},
	)
}

func (m *Model) applyUpdateDownloaded(msg updateDownloadedMsg) tea.Cmd {
	if msg.err != nil {
		m.update.state = updateFailed
		m.update.err = msg.err
		m.setError(fmt.Sprintf("download update: %v", msg.err))
		return nil
	}
	m.update.asset = msg.asset
	m.update.state = updateInstalling

	// No second ticker: the one started with the download runs through both
	// phases, since the bar spans the whole operation rather than restarting
	// halfway. Batching another here would just double the redraw rate.
	updater, asset := m.updater, msg.asset
	return func() tea.Msg {
		executable, err := currentExecutable()
		if err != nil {
			return updateInstalledMsg{err: err}
		}
		result, err := updater.Install(asset, executable)
		return updateInstalledMsg{result: result, err: err}
	}
}

func (m *Model) applyUpdateInstalled(msg updateInstalledMsg) tea.Cmd {
	// A failure is shown the moment it happens: there is nothing to make look
	// good, and holding an error back behind an animation is just a delay.
	if msg.err != nil {
		return m.finishUpdateInstall(msg)
	}
	// Holding only makes sense while a bar is actually running. Without this
	// a result arriving in any other state would be parked waiting for a tick
	// that never comes, and the install would silently never land.
	if m.update.state != updateDownloading && m.update.state != updateInstalling {
		return m.finishUpdateInstall(msg)
	}
	// Success waits for the bar. applyUpdateTick finalises once it lands on
	// 100%, which is never before updateMinDuration has passed.
	m.update.pending = &msg
	return m.applyUpdateTick()
}

// finishUpdateInstall folds the real result in, once the bar has caught up.
func (m *Model) finishUpdateInstall(msg updateInstalledMsg) tea.Cmd {
	m.update.pending = nil
	m.update.percent = 100
	if msg.err != nil {
		m.update.state = updateFailed
		m.update.err = msg.err
		m.setError(fmt.Sprintf("install update: %v", msg.err))
		return nil
	}
	m.update.manualCommand = msg.result.ManualCommand
	m.update.removeCommand = msg.result.ShadowedCommand
	// RequiresManual comes back with a nil error: the download succeeded and
	// was verified, only the install target was not writable. Reporting that
	// as a failure would tell the user the update broke when the fix is one
	// printed command, so it gets its own state.
	if msg.result.RequiresManual {
		m.update.state = updateNeedsElevation
		m.setError("update downloaded, but installing it needs a command run outside TideFTP")
		return nil
	}
	m.update.state = updateInstalled
	if msg.result.Restartable {
		m.restartExec = msg.result.ExecutablePath
	}
	// The version just installed is by definition not one to be nagged about.
	m.updates.DismissedVersion = ""
	m.logs = append(m.logs, "updated to "+m.update.latest.Version)
	m.setStatus("updated to " + m.update.latest.Version + " — restart to use it")
	return m.persist()
}

// dismissUpdate suppresses the notice for this exact version. A later release
// still surfaces, which is the point of recording the version rather than a
// bare "don't tell me" flag.
func (m *Model) dismissUpdate() tea.Cmd {
	if m.update.latest.Version == "" {
		return nil
	}
	m.updates.DismissedVersion = m.update.latest.Version
	m.setStatus("ignoring " + m.update.latest.Version + " — you'll hear about the next release")
	return m.persist()
}

// currentExecutable resolves the running binary's path, which is what Install
// replaces. It is a variable so a test can point an install at a scratch file
// instead of the test binary itself.
var currentExecutable = os.Executable

// applyUpdateTick redraws the progress bar and decides when the install is
// allowed to finish.
//
// The bar is driven by elapsed time rather than by counting ticks, so it
// crosses in updateMinDuration regardless of how the scheduler treats the
// ticker. It is held at 99 until the real work reports back, so it can never
// sit at 100% while something is still happening — and once the work is done
// it always finishes at a full 100 before the overlay moves on.
func (m *Model) applyUpdateTick() tea.Cmd {
	if m.update.state != updateDownloading && m.update.state != updateInstalling {
		return nil
	}
	elapsed := time.Since(m.update.startedAt)
	percent := int(elapsed * 100 / updateMinDuration)
	if m.update.pending == nil {
		// Still working. Crawl the last stretch rather than parking on 100.
		percent = min(percent, 99)
	}
	m.update.percent = min(100, max(0, percent))

	if m.update.pending != nil && m.update.percent >= 100 {
		return m.finishUpdateInstall(*m.update.pending)
	}
	return updateTickCmd()
}

// RestartExecPath is the binary main should exec after the program exits, or
// "" when no update was installed this session. It is the only reason the UI
// package exposes anything about updating.
func (m Model) RestartExecPath() string { return m.restartExec }

// openUpdateOverlay shows what a check found. It is bound to U, and reports
// rather than opening on nothing when there is no update to act on — a key
// that silently does nothing reads as broken.
func (m *Model) openUpdateOverlay() {
	switch {
	case m.update.state == updateInstalled, m.update.state == updateNeedsElevation:
		// Still worth showing: one is waiting on a restart, the other on a
		// command the user has to run.
	case m.update.state == updateDownloading, m.update.state == updateInstalling:
		// Already working; the overlay is where the progress is.
	case m.update.latest.Version == "":
		if m.update.checked {
			m.setStatus("TideFTP is up to date (" + m.version + ")")
		} else {
			m.setStatus("no update check has run yet — check from settings (,)")
		}
		return
	}
	m.updateBusyAck = false
	m.overlay = overlayUpdate
}

// handleUpdateKey routes keys while overlayUpdate is open. It needs its own
// handler rather than the generic confirm block because enter means something
// different in every state — install, nothing, or quit-and-restart — and must
// be swallowed entirely while the download or install is in flight.
func (m *Model) handleUpdateKey(msg tea.KeyMsg) tea.Cmd {
	switch m.update.state {
	case updateDownloading, updateInstalling:
		// Busy. Everything is swallowed; ctrl+c is handled before this runs,
		// so there is still a way out.
		return nil
	case updateInstalled:
		switch msg.String() {
		case "enter", "y":
			// quitNow rather than tea.Quit directly, so the connection is
			// closed cleanly first. main execs the new binary afterwards.
			return m.quitNow()
		case "esc", "q", "n":
			m.overlay = overlayNone
			m.setStatus("restart whenever you like — the new version is already in place")
		}
		return nil
	case updateNeedsElevation, updateFailed:
		switch msg.String() {
		case "c":
			if m.update.manualCommand != "" {
				m.overlay = overlayNone
				return copyToClipboardCmd(m.update.manualCommand, 1)
			}
		case "esc", "q", "n", "enter":
			m.overlay = overlayNone
		}
		return nil
	default:
		switch msg.String() {
		case "enter", "y":
			return m.confirmUpdateInstall()
		case "i":
			m.overlay = overlayNone
			return m.dismissUpdate()
		case "esc", "q", "n":
			m.overlay = overlayNone
			m.updateBusyAck = false
			m.setStatus("update postponed")
		}
	}
	return nil
}

// confirmUpdateInstall starts the install, asking twice when transfers are
// still moving. Installing swaps the binary and the restart afterwards drops
// whatever was in flight, which is worth one more keypress — but it warns
// rather than refuses: it is the user's call, and a long mirror should not be
// able to lock them out of updating.
func (m *Model) confirmUpdateInstall() tea.Cmd {
	if m.queueBusy() && !m.updateBusyAck {
		m.updateBusyAck = true
		m.setStatus("transfers are still running — press enter again to install anyway")
		return nil
	}
	m.updateBusyAck = false
	return m.startUpdateInstall()
}

// updateOverlayTitle names the panel for the state it is showing.
func updateOverlayTitle(state updateState) string {
	switch state {
	case updateDownloading, updateInstalling:
		return "installing update"
	case updateInstalled:
		return "update installed"
	case updateNeedsElevation:
		return "manual update"
	case updateFailed:
		return "update failed"
	default:
		return "install update?"
	}
}

// updateProgressBar draws the bar the way TideMail's does: accent-coloured
// blocks for the filled part, muted shading for the rest, spanning the full
// content width. The percentage is not in the bar — it belongs in the label
// above it, so the bar itself stays a clean run of blocks.
func updateProgressBar(renderer tideui.Renderer, percent, width int) string {
	width = max(1, width)
	percent = min(100, max(0, percent))
	filled := min(width, max(0, width*percent/100))
	full := lipgloss.NewStyle().Foreground(renderer.Styles.Theme.BorderFocus).Render(strings.Repeat("█", filled))
	empty := lipgloss.NewStyle().Foreground(renderer.Styles.Theme.Dimmed).Render(strings.Repeat("░", width-filled))
	return full + empty
}

// relativeSince renders how long ago a check ran, coarsely — the settings row
// wants "2h ago", not a timestamp to read carefully.
func relativeSince(when time.Time) string {
	if when.IsZero() {
		return "never"
	}
	elapsed := time.Since(when)
	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(elapsed.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(elapsed.Hours())/24)
	}
}

// activateUpdateRow is what enter does on the Settings "Updates" row. The row
// is one control whose meaning follows the state, so this is where "check
// now", "install", and "show me the manual command" all land.
func (m *Model) activateUpdateRow() tea.Cmd {
	switch m.update.state {
	case updateChecking, updateDownloading, updateInstalling:
		return nil
	case updateAvailable, updateInstalled, updateNeedsElevation:
		m.openUpdateOverlay()
		return nil
	default:
		m.update.state = updateChecking
		m.setStatus("checking for updates…")
		return m.checkForUpdatesCmd(true)
	}
}
