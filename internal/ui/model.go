package ui

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"tideftp/internal/config"
	"tideftp/internal/credstore"
	"tideftp/internal/domain"
	"tideftp/internal/session"
	"tideftp/internal/transfer"
	"tideftp/internal/vfs"
)

type focusPane int

const (
	focusLocal focusPane = iota
	focusRemote
	focusQueue
)

type bottomTab int

const (
	tabQueue bottomTab = iota
	tabActive
	tabFailed
	tabHistory
	tabLog
)

type overlayMode int

const (
	overlayNone overlayMode = iota
	overlayHelp
	overlayConnect
	overlayConflict
	overlayTheme
	overlayPreflight
	overlaySettings
)

// paneID names a file pane for listing requests. It is deliberately separate
// from focusPane: a listing can land in a pane that is not focused.
type paneID int

const (
	paneLocal paneID = iota
	paneRemote
)

func (p paneID) String() string {
	if p == paneLocal {
		return "local"
	}
	return "remote"
}

// listingKind says what to do with the cursor and selection when a listing
// arrives. Walking into a directory starts fresh; re-reading the directory you
// are already in must not move the cursor or drop what is selected.
type listingKind int

const (
	listingNavigate listingKind = iota
	listingRefresh
)

// connState is where the remote side of the app currently stands. The local
// pane works in every one of these states; only the remote pane and the
// transfer queue depend on a live connection.
type connState int

const (
	connDisconnected connState = iota
	connConnecting
	connConnected
	connFailed
)

func (c connState) String() string {
	switch c {
	case connConnecting:
		return "connecting"
	case connConnected:
		return "connected"
	case connFailed:
		return "failed"
	default:
		return "disconnected"
	}
}

// connectedMsg carries a live connection back to the UI goroutine.
type connectedMsg struct {
	target session.Target
	conn   session.Conn
}

// connectFailedMsg reports a connect attempt that never opened.
type connectFailedMsg struct {
	target session.Target
	err    error
}

// disconnectedMsg reports a connection that has ended: err is nil when the
// user asked for it, and the reason when the server went away.
type disconnectedMsg struct {
	conn session.Conn
	err  error
}

// listingMsg is the reply to one List request.
type listingMsg struct {
	pane    paneID
	token   int
	kind    listingKind
	path    string
	entries []domain.Entry
	err     error
}

type filePane struct {
	title      string
	path       string
	entries    []domain.Entry
	cursor     int
	offset     int
	showHidden bool
	selected   map[string]bool

	// requestToken is the id of the most recent listing request. Replies
	// carrying an older token are stale — two quick Enter presses put two
	// listings in flight, and the slower one must not overwrite the newer
	// directory — so they are dropped.
	requestToken int
	// loading is set while a listing is in flight, and pendingPath is the
	// directory being loaded. The pane keeps showing its current contents
	// until the reply lands, so a failed listing changes nothing.
	loading     bool
	pendingPath string
}

// beginRequest marks the pane as waiting for dirPath and returns the token the
// reply must carry to be accepted.
func (p *filePane) beginRequest(dirPath string) int {
	p.requestToken++
	p.loading = true
	p.pendingPath = dirPath
	return p.requestToken
}

// displayPath is what the pane header shows: the directory being loaded while
// a request is in flight, otherwise the one on screen.
func (p filePane) displayPath() string {
	if p.loading && p.pendingPath != "" {
		return p.pendingPath
	}
	return p.path
}

type Model struct {
	width, height int
	focus         focusPane
	overlay       overlayMode

	local   filePane
	remote  filePane
	localFS vfs.FS

	// Everything below is valid only while state is connConnected. remoteFS
	// and engine come from conn and are cleared with it, so that a dropped
	// connection cannot leave a stale adapter wired into the UI.
	dialer  session.Dialer
	targets []session.Target
	target  session.Target

	// profiles are the user's saved connection targets, loaded from config on
	// startup and offered by the connect form's Profile field. They are
	// distinct from targets: targets is what to auto-connect to at startup
	// (from --host or the demo adapter), profiles is what the user chose to
	// keep for later.
	profiles []session.Target

	// Connect form state, editable while the connect overlay is open.
	connectForm   connectFormValue
	connectField  connectField
	connectCursor int

	conn     session.Conn
	remoteFS vfs.FS
	engine   transfer.Engine
	state    connState
	connErr  error

	transfers      []domain.Transfer
	nextTransferID int
	maxParallel    int
	bottomTab      bottomTab
	bottomOffset   int
	// bottomCursor selects one row within the current bottom-pane tab's
	// transfers, the target for a contextual x (cancel)/R (retry). It has
	// no meaning on tabLog, which is plain scrolling text with no rows to
	// select.
	bottomCursor int
	// preflight holds the result of walking a folder queued for transfer,
	// while overlayPreflight is asking the user to confirm it. Nil the rest
	// of the time.
	preflight *preflightScan
	logs      []string

	theme       tideui.Theme
	themePicker tideui.ThemePicker
	density     tideui.Density
	shadow      bool
	showIcons   bool
	// settingsCursor selects one row in the settings overlay (see
	// settings.go), the same way connectField selects a row in the connect
	// form.
	settingsCursor int

	fileSplit   tideui.PaneRatio
	bottomSplit tideui.PaneRatio

	// save persists the current settings, if non-nil. It is a seam set by
	// the caller (main) so the UI never touches the filesystem; tests leave
	// it nil and persistence is simply skipped.
	save config.SaveFunc
	// creds remembers a saved profile's password in the OS keyring, if
	// non-nil. Like save, it is a seam: nil means the feature is
	// unavailable, and the connect form's Remember field simply does not
	// appear (see connectFieldVisible).
	creds credstore.Store

	status    string
	statusErr bool
}

// transferStreamClosed arrives when the engine has shut down and will send no
// more events, so the UI stops waiting on the channel.
type transferStreamClosed struct{}

// defaultParallelTransfers is how many transfers run at once when no config
// value overrides Model.maxParallel.
const defaultParallelTransfers = 2

// maxParallelCap bounds how high +/- can push Model.maxParallel. It exists so
// a runaway keypress cannot start hundreds of concurrent transfers; the pools
// behind ftpsession/sftpsession have their own smaller caps too, but this
// keeps the number sane before it ever reaches them.
const maxParallelCap = 8

// dialTimeout bounds a connect attempt, which can otherwise hang as long as
// the network cares to.
const dialTimeout = 30 * time.Second

// listTimeout bounds a single directory listing. A real server can accept a
// connection and then never answer; without this the pane would sit in its
// loading state forever.
const listTimeout = 20 * time.Second

// firstFileRow is the screen row of the first entry in a file pane:
// topbar (1) + pane top border (1) + pane title header (1) + column
// header (1). Mouse clicks above it are not on an entry.
const firstFileRow = 4

// NewModel builds the UI over a local filesystem and a dialer. It starts
// disconnected; Init dials the first target so the app opens onto something.
//
// cfg supplies the persisted settings. Pass config.Default() or a config.Load
// result — the zero Config would silently turn off shadow and icons, so it is
// not a sensible input. save, when non-nil, is called to persist every change
// the user makes to those settings.
func NewModel(local vfs.FS, dialer session.Dialer, targets []session.Target, cfg config.Config, save config.SaveFunc, creds credstore.Store) Model {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	density := tideui.Compact
	if tideui.Density(cfg.Density) == tideui.Comfortable {
		density = tideui.Comfortable
	}
	maxParallel := cfg.MaxParallel
	if maxParallel < 1 {
		maxParallel = defaultParallelTransfers
	}
	model := Model{
		focus: focusLocal,
		local: filePane{
			title:      "Local",
			path:       cwd,
			selected:   map[string]bool{},
			showHidden: false,
		},
		remote: filePane{
			title:      "Remote",
			path:       "",
			selected:   map[string]bool{},
			showHidden: false,
		},
		localFS:        local,
		dialer:         dialer,
		targets:        targets,
		profiles:       profilesFromConfig(cfg.Profiles),
		state:          connDisconnected,
		nextTransferID: 1,
		maxParallel:    maxParallel,
		theme:          themeByName(cfg.Theme),
		density:        density,
		shadow:         cfg.Shadow,
		showIcons:      cfg.ShowIcons,
		fileSplit:      tideui.NewPaneRatio(tideui.PaneRatioOptions{Initial: cfg.Layout.FileSplit, Min: 0.25, Max: 0.75, Step: 0.03}),
		bottomSplit:    tideui.NewPaneRatio(tideui.PaneRatioOptions{Initial: cfg.Layout.BottomSplit, Min: 0.15, Max: 0.50, Step: 0.03}),
		save:           save,
		creds:          creds,
		logs:           []string{"redacted logs enabled"},
		status:         "ready",
	}
	model.themePicker = tideui.NewThemePicker(tideui.ThemePickerOptions{
		Themes:       appThemes(),
		InitialTheme: model.theme.Name,
		Title:        "THEMES",
	})
	// Listings are asynchronous, and a constructor cannot return commands, so
	// the local pane starts in its loading state and Init issues the request.
	// The remote pane has nothing to list until a connection exists.
	model.local.beginRequest(model.local.path)
	if len(targets) > 0 {
		model.target = targets[0]
		model.state = connConnecting
	}
	return model
}

// snapshotConfig captures the current settings for persistence.
func (m Model) snapshotConfig() config.Config {
	return config.Config{
		Theme:       m.theme.Name,
		Density:     string(m.density),
		Shadow:      m.shadow,
		ShowIcons:   m.showIcons,
		MaxParallel: m.maxParallel,
		Layout: config.Layout{
			FileSplit:   m.fileSplit.Value(),
			BottomSplit: m.bottomSplit.Value(),
		},
		Profiles: profilesToConfig(m.profiles),
	}
}

// profilesFromConfig converts persisted profiles into the session.Target shape
// the connect form and dialer use.
func profilesFromConfig(profiles []config.Profile) []session.Target {
	if len(profiles) == 0 {
		return nil
	}
	targets := make([]session.Target, len(profiles))
	for i, p := range profiles {
		targets[i] = session.Target{
			Name: p.Name, Protocol: p.Protocol, Host: p.Host,
			Port: p.Port, User: p.User, StartPath: p.StartPath,
		}
	}
	return targets
}

// profilesToConfig converts saved connection targets into the config schema
// for persistence.
func profilesToConfig(targets []session.Target) []config.Profile {
	if len(targets) == 0 {
		return nil
	}
	profiles := make([]config.Profile, len(targets))
	for i, t := range targets {
		profiles[i] = config.Profile{
			Name: t.Name, Protocol: t.Protocol, Host: t.Host,
			Port: t.Port, User: t.User, StartPath: t.StartPath,
		}
	}
	return profiles
}

// persist returns a command that saves the current settings, if a saver was
// configured. The save itself runs off the Update goroutine and its failure
// is swallowed on purpose: persistence is best-effort, and a full disk or a
// read-only home directory must never interrupt the UI or stall a keypress
// waiting on disk I/O — config.Save's mkdir, marshal, temp file, and rename
// are exactly the kind of blocking work a tea.Cmd exists to move off Update.
// snapshotConfig runs synchronously here, before the command is returned, so
// the command captures the settings as they were at the moment of the
// change, not whatever they happen to be by the time the command runs.
func (m Model) persist() tea.Cmd {
	if m.save == nil {
		return nil
	}
	save, cfg := m.save, m.snapshotConfig()
	return func() tea.Msg {
		_ = save(cfg)
		return nil
	}
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		tea.SetWindowTitle("TideFTP"),
		listCmd(m.localFS, paneLocal, m.local.requestToken, m.local.path, m.local.showHidden, listingNavigate),
	}
	// The transfer event pump belongs to a connection, so it starts on connect
	// rather than here.
	if m.state == connConnecting {
		cmds = append(cmds, dialCmd(m.dialer, m.target, session.Credentials{}))
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case connectedMsg:
		return m, m.applyConnected(msg)
	case connectFailedMsg:
		m.applyConnectFailed(msg)
		return m, nil
	case disconnectedMsg:
		wasAtBottom := m.isAtBottomPane()
		m.applyDisconnected(msg)
		m.settleBottomOffset(wasAtBottom)
		return m, nil
	case listingMsg:
		wasAtBottom := m.isAtBottomPane()
		m.applyListing(msg)
		m.settleBottomOffset(wasAtBottom)
		return m, nil
	case storedCredentialMsg:
		if msg.token != m.connectForm.credToken {
			return m, nil // superseded by a later profile selection
		}
		if msg.err != nil {
			m.setError(fmt.Sprintf("load saved password: %v", msg.err))
			return m, nil
		}
		if msg.ok {
			m.connectForm.password = msg.password
			m.connectForm.remember = true
			m.markConnectFieldsFresh(connectFieldPassword)
		}
		return m, nil
	case credentialSyncMsg:
		// Success says nothing new: the "saved profile"/"deleted profile"
		// status set synchronously already covers it, and this must not
		// clobber that with a redundant message. A failure does need
		// reporting — the user otherwise has no way to know a password they
		// asked to be remembered was not.
		if msg.err != nil {
			m.setError(fmt.Sprintf("password not saved: %v", msg.err))
		}
		return m, nil
	case preflightScanMsg:
		wasAtBottom := m.isAtBottomPane()
		m.applyPreflightScan(msg)
		m.settleBottomOffset(wasAtBottom)
		return m, nil
	case transfer.Event:
		// Applying an event can finish a transfer, which frees a slot for the
		// next queued one, which is why startQueuedTransfers runs here too.
		wasAtBottom := m.isAtBottomPane()
		m.applyTransferEvent(msg)
		m.startQueuedTransfers()
		m.settleBottomOffset(wasAtBottom)
		if !m.connected() {
			return m, nil
		}
		return m, waitForTransferEvent(m.engine.Events())
	case transferStreamClosed:
		return m, nil
	case tea.MouseMsg:
		return m.updateMouse(msg)
	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

// updateKey handles a key press. Actions taken here (queuing a transfer,
// logging a connect attempt, a transfer advancing) can grow or shrink the
// row count for whichever bottom-pane tab is active; the deferred settle
// call keeps the view pinned to the bottom if it was already there
// (auto-follow), on every return path, without needing it duplicated at
// each early return. Two escape hatches opt out of the follow snap: tabSwitch
// (setBottomTab already picks the right offset for a freshly opened tab) and
// manualScroll (the user is deliberately scrolling the bottom pane, so their
// input must not be overridden by auto-follow — it's still clamped though).
func (m Model) updateKey(msg tea.KeyMsg) (result tea.Model, cmd tea.Cmd) {
	wasAtBottom := m.isAtBottomPane()
	tabSwitch := false
	manualScroll := false
	defer func() {
		next, ok := result.(Model)
		if !ok || tabSwitch {
			return
		}
		if manualScroll {
			next.clampBottomOffset()
		} else {
			next.settleBottomOffset(wasAtBottom)
		}
		result = next
	}()

	if m.overlay == overlayTheme {
		action := m.themePicker.Update(msg)
		m.theme = m.themePicker.PreviewTheme()
		switch action {
		case tideui.ThemePickerConfirm:
			m.overlay = overlayNone
			m.theme = m.themePicker.ConfirmedTheme()
			m.setStatus("theme set to " + m.theme.Name)
			return m, m.persist()
		case tideui.ThemePickerCancel:
			m.overlay = overlayNone
			m.theme = m.themePicker.ConfirmedTheme()
			m.setStatus("theme unchanged")
		}
		return m, nil
	}
	if m.overlay == overlayConnect {
		return m, m.handleConnectKey(msg)
	}
	if m.overlay == overlaySettings {
		return m, m.handleSettingsKey(msg)
	}
	if m.overlay != overlayNone {
		switch msg.String() {
		case "esc", "q", "n":
			if m.overlay == overlayPreflight {
				m.preflight = nil
			}
			m.overlay = overlayNone
			m.setStatus("cancelled")
		case "enter", "y":
			switch m.overlay {
			case overlayConflict:
				m.overlay = overlayNone
				return m, m.queueFocusedTransfer()
			case overlayPreflight:
				m.overlay = overlayNone
				return m, m.confirmPreflightQueue()
			case overlayHelp:
				m.overlay = overlayNone
			}
		}
		return m, nil
	}

	switch msg.String() {
	case "ctrl+c", "q":
		if m.conn != nil {
			return m, tea.Sequence(closeConnCmd(m.conn), tea.Quit)
		}
		return m, tea.Quit
	case "tab":
		m.focus = (m.focus + 1) % 3
	case "shift+tab":
		m.focus = (m.focus + 2) % 3
	case "up", "k":
		manualScroll = m.focus == focusQueue
		m.moveCursor(-1)
	case "down", "j":
		manualScroll = m.focus == focusQueue
		m.moveCursor(1)
	case "pgup":
		manualScroll = m.focus == focusQueue
		m.moveCursor(-10)
	case "pgdown":
		manualScroll = m.focus == focusQueue
		m.moveCursor(10)
	case "enter":
		cmd = m.activateCursor()
	case "backspace", "h":
		cmd = m.parentDir()
	case " ":
		m.toggleSelection()
	case "esc":
		m.clearSelection()
	case "ctrl+a":
		m.selectAll()
	case ".":
		cmd = m.toggleHidden()
	case "i":
		m.showIcons = !m.showIcons
		if m.showIcons {
			m.setStatus("icons on")
		} else {
			m.setStatus("icons off")
		}
		cmd = m.persist()
	case "u":
		cmd = m.queueUpload()
	case "d":
		cmd = m.queueDownload()
	case "o":
		m.overlay = overlayConflict
	case "x":
		m.cancelActiveTransfers()
	case "R":
		cmd = m.retrySelectedTransfer()
	case "+":
		cmd = m.adjustMaxParallel(1)
	case "-":
		cmd = m.adjustMaxParallel(-1)
	case "r":
		cmd = m.refresh()
	case "c":
		cmd = m.openConnectForm()
	case "t":
		m.overlay = overlayTheme
		m.themePicker.Open(m.theme.Name)
	case ",":
		m.overlay = overlaySettings
		m.settingsCursor = 0
	case "?":
		m.overlay = overlayHelp
	case "shift+left":
		m.fileSplit.Shrink()
		m.setStatus("local pane narrower")
		cmd = m.persist()
	case "shift+right":
		m.fileSplit.Grow()
		m.setStatus("local pane wider")
		cmd = m.persist()
	case "shift+up":
		m.bottomSplit.Grow()
		m.setStatus("transfer pane taller")
		cmd = m.persist()
	case "shift+down":
		m.bottomSplit.Shrink()
		m.setStatus("transfer pane shorter")
		cmd = m.persist()
	case "ctrl+0":
		m.fileSplit = tideui.NewPaneRatio(tideui.PaneRatioOptions{Initial: 0.5, Min: 0.25, Max: 0.75, Step: 0.03})
		m.bottomSplit = tideui.NewPaneRatio(tideui.PaneRatioOptions{Initial: 0.28, Min: 0.15, Max: 0.50, Step: 0.03})
		m.setStatus("layout reset")
		cmd = m.persist()
	case "1":
		tabSwitch = true
		m.setBottomTab(tabQueue)
	case "2":
		tabSwitch = true
		m.setBottomTab(tabActive)
	case "3":
		tabSwitch = true
		m.setBottomTab(tabFailed)
	case "4":
		tabSwitch = true
		m.setBottomTab(tabHistory)
	case "5":
		tabSwitch = true
		m.setBottomTab(tabLog)
	}
	m.clampCursors()
	return m, cmd
}

func (m Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress && msg.Action != tea.MouseActionMotion {
		return m, nil
	}
	// topPaneHeight is what View actually draws with, so the hit test and
	// the drawn boundary agree even when the layout clamp bites.
	topHeight := m.topPaneHeight()
	bottomStart := 1 + topHeight
	localWidth := max(20, int(float64(m.width)*m.fileSplit.Value()))
	switch {
	case msg.Y < bottomStart && msg.X < localWidth:
		m.focus = focusLocal
		m.cursorFromMouse(&m.local, msg.Y)
	case msg.Y < bottomStart:
		m.focus = focusRemote
		m.cursorFromMouse(&m.remote, msg.Y)
	default:
		m.focus = focusQueue
	}
	return m, nil
}

// connect starts dialing target as creds, tearing down any existing
// connection first.
func (m *Model) connect(target session.Target, creds session.Credentials) tea.Cmd {
	cmds := []tea.Cmd{}
	if m.conn != nil {
		cmds = append(cmds, closeConnCmd(m.conn))
		m.clearConnection("reconnecting")
	}
	m.target = target
	m.state = connConnecting
	m.connErr = nil
	m.setStatus("connecting to " + target.Label())
	m.logs = append(m.logs, "connect "+target.Address()+" as "+target.User+" (credentials redacted)")
	return tea.Batch(append(cmds, dialCmd(m.dialer, target, creds))...)
}

// disconnect ends the current connection at the user's request.
func (m *Model) disconnect() tea.Cmd {
	if m.conn == nil {
		m.setError("not connected")
		return nil
	}
	conn := m.conn
	m.setStatus("disconnecting from " + m.target.Label())
	return closeConnCmd(conn)
}

func dialCmd(dialer session.Dialer, target session.Target, creds session.Credentials) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
		defer cancel()
		conn, err := dialer.Dial(ctx, target, creds)
		if err != nil {
			return connectFailedMsg{target: target, err: err}
		}
		return connectedMsg{target: target, conn: conn}
	}
}

// watchConnCmd parks on the connection's Done channel and reports the end,
// whether that was a Close we asked for or the server going away.
func watchConnCmd(conn session.Conn) tea.Cmd {
	return func() tea.Msg {
		err := <-conn.Done()
		return disconnectedMsg{conn: conn, err: err}
	}
}

func closeConnCmd(conn session.Conn) tea.Cmd {
	return func() tea.Msg {
		_ = conn.Close()
		return nil
	}
}

// applyConnected wires a live connection into the UI: its filesystem, its
// transfer engine, the event pump, the drop watcher, and the first listing.
func (m *Model) applyConnected(msg connectedMsg) tea.Cmd {
	// A connection that arrives after the user moved on is closed, not used.
	if m.state != connConnecting || msg.target != m.target {
		return closeConnCmd(msg.conn)
	}
	m.conn = msg.conn
	m.remoteFS = msg.conn.FS()
	m.engine = msg.conn.Engine()
	m.state = connConnected
	m.connErr = nil
	m.setStatus("connected to " + msg.target.Label())
	m.logs = append(m.logs, "connected "+msg.target.Address())

	m.remote.reset()
	m.remote.path = ""
	return tea.Batch(
		waitForTransferEvent(msg.conn.Engine().Events()),
		watchConnCmd(msg.conn),
		m.requestListing(paneRemote, msg.target.Home(), listingNavigate),
	)
}

func (m *Model) applyConnectFailed(msg connectFailedMsg) {
	if m.state != connConnecting || msg.target != m.target {
		return
	}
	m.state = connFailed
	m.connErr = msg.err
	m.setError(fmt.Sprintf("connect %s: %v", msg.target.Label(), msg.err))
}

// applyDisconnected tears the connection down. Anything in flight is marked
// failed: the bytes stopped moving whether or not the user asked them to.
func (m *Model) applyDisconnected(msg disconnectedMsg) {
	if msg.conn != m.conn {
		// A previous connection finishing after we moved on. Whatever it was
		// carrying was already failed when we moved on — see connect.
		return
	}
	reason := "disconnected"
	if msg.err != nil {
		reason = msg.err.Error()
	}
	m.clearConnection(reason)
	if msg.err != nil {
		m.state = connFailed
		m.connErr = msg.err
		m.setError("connection lost: " + msg.err.Error())
	} else {
		m.state = connDisconnected
		m.setStatus("disconnected")
	}
	m.logs = append(m.logs, "connection ended: "+reason)
}

// clearConnection drops every reference to the live connection, so a stale
// adapter can never be used after the connection has ended, and fails any
// transfer that was still relying on it — Queued or Active — with reason.
//
// It has to do the failing itself rather than leaving it to the disconnect
// message that will eventually arrive: connect calls this synchronously to
// tear down the previous connection before dialing the next one, and by the
// time that old connection's own disconnectedMsg lands, m.conn already names
// the new connection, so applyDisconnected's staleness check would discard
// it — and the transfers it was carrying would stay Active forever.
func (m *Model) clearConnection(reason string) {
	for i := range m.transfers {
		if m.transfers[i].Status == domain.Queued || m.transfers[i].Status == domain.Active {
			m.transfers[i].Status = domain.Failed
			m.transfers[i].FinishedAt = time.Now()
			m.transfers[i].Message = reason
		}
	}
	m.conn = nil
	m.remoteFS = nil
	m.engine = nil
	m.state = connDisconnected
	m.remote.entries = nil
	m.remote.path = ""
	m.remote.loading = false
	m.remote.pendingPath = ""
	m.remote.reset()
	if m.focus == focusRemote {
		m.focus = focusLocal
	}
}

// connected reports whether the remote pane and the transfer queue can do
// anything at all.
func (m Model) connected() bool {
	return m.state == connConnected && m.conn != nil && m.remoteFS != nil && m.engine != nil
}

// refresh re-reads both panes in place, keeping cursors and selections.
func (m *Model) refresh() tea.Cmd {
	m.setStatus("refreshing")
	cmds := []tea.Cmd{m.requestListing(paneLocal, m.local.path, listingRefresh)}
	if m.connected() {
		cmds = append(cmds, m.requestListing(paneRemote, m.remote.path, listingRefresh))
	}
	return tea.Batch(cmds...)
}

// requestListing issues a List for dirPath and returns the command that runs
// it off the UI goroutine. The pane keeps its current contents until the reply
// arrives, so a listing that fails or never answers leaves the pane usable.
func (m *Model) requestListing(pane paneID, dirPath string, kind listingKind) tea.Cmd {
	if pane == paneRemote && m.remoteFS == nil {
		m.setError("not connected")
		return nil
	}
	target := m.filePaneByID(pane)
	token := target.beginRequest(dirPath)
	return listCmd(m.fsByID(pane), pane, token, dirPath, target.showHidden, kind)
}

// listCmd is a free function rather than a method because Init has a value
// receiver and cannot record the request on the model; NewModel does that part.
func listCmd(fs vfs.FS, pane paneID, token int, dirPath string, showHidden bool, kind listingKind) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
		defer cancel()
		entries, err := fs.List(ctx, dirPath, showHidden)
		return listingMsg{pane: pane, token: token, kind: kind, path: dirPath, entries: entries, err: err}
	}
}

// applyListing folds one reply into its pane, ignoring stale replies and
// leaving the pane untouched when the listing failed.
func (m *Model) applyListing(msg listingMsg) {
	target := m.filePaneByID(msg.pane)
	if msg.token != target.requestToken {
		return
	}
	target.loading = false
	target.pendingPath = ""
	if msg.err != nil {
		m.setError(fmt.Sprintf("%s: %v", msg.pane, msg.err))
		return
	}
	target.entries = msg.entries
	switch msg.kind {
	case listingNavigate:
		target.path = msg.path
		target.reset()
	case listingRefresh:
		target.clamp(m.filePaneVisibleRows())
	}
	// Deliberately not clearing an error status here: refresh lists both
	// panes, so a success on one would wipe a genuine failure reported by the
	// other. The next action replaces the status anyway.
}

// navigateTo walks a pane to dirPath. The path is not committed until the
// listing succeeds, so a directory that cannot be read leaves the pane where
// it was.
func (m *Model) navigateTo(pane paneID, dirPath string) tea.Cmd {
	return m.requestListing(pane, dirPath, listingNavigate)
}

func (m *Model) filePaneByID(pane paneID) *filePane {
	if pane == paneLocal {
		return &m.local
	}
	return &m.remote
}

func (m Model) fsByID(pane paneID) vfs.FS {
	if pane == paneLocal {
		return m.localFS
	}
	return m.remoteFS
}

// focusedPaneID reports which file pane has focus, and whether one does at all
// (the transfers pane is not a file pane).
func (m Model) focusedPaneID() (paneID, bool) {
	switch m.focus {
	case focusLocal:
		return paneLocal, true
	case focusRemote:
		return paneRemote, true
	default:
		return paneLocal, false
	}
}

func (m *Model) moveCursor(delta int) {
	switch m.focus {
	case focusLocal:
		m.local.cursor += delta
	case focusRemote:
		m.remote.cursor += delta
	case focusQueue:
		if m.bottomTab == tabLog {
			// The log has lines to scroll through, not rows to select.
			m.bottomOffset = max(0, m.bottomOffset+delta)
			return
		}
		m.bottomCursor += delta
	}
}

// activateCursor opens the directory under the cursor in the focused pane.
func (m *Model) activateCursor() tea.Cmd {
	pane, ok := m.focusedPaneID()
	if !ok {
		return nil
	}
	target := m.filePaneByID(pane)
	entry, found := target.current()
	if !found || !entry.IsDir() {
		return nil
	}
	return m.navigateTo(pane, m.fsByID(pane).Child(target.path, entry.Name))
}

// parentDir walks the focused pane up one level, doing nothing at the root.
func (m *Model) parentDir() tea.Cmd {
	pane, ok := m.focusedPaneID()
	if !ok {
		return nil
	}
	target := m.filePaneByID(pane)
	parent := m.fsByID(pane).Parent(target.path)
	if parent == target.path {
		return nil
	}
	return m.navigateTo(pane, parent)
}

func (m *Model) toggleSelection() {
	pane := m.focusedFilePane()
	if pane == nil {
		return
	}
	entry, ok := pane.current()
	if !ok {
		return
	}
	pane.selected[entry.Name] = !pane.selected[entry.Name]
	if !pane.selected[entry.Name] {
		delete(pane.selected, entry.Name)
	}
}

func (m *Model) clearSelection() {
	if pane := m.focusedFilePane(); pane != nil {
		pane.selected = map[string]bool{}
		m.setStatus("selection cleared")
	}
}

func (m *Model) selectAll() {
	if pane := m.focusedFilePane(); pane != nil {
		for _, entry := range pane.entries {
			pane.selected[entry.Name] = true
		}
		m.setStatus(fmt.Sprintf("selected %d item(s)", len(pane.selected)))
	}
}

func (m *Model) toggleHidden() tea.Cmd {
	pane, ok := m.focusedPaneID()
	if !ok {
		return nil
	}
	target := m.filePaneByID(pane)
	target.showHidden = !target.showHidden
	m.setStatus("hidden files toggled")
	// Only the focused pane's setting changed, so only it needs re-reading.
	return m.requestListing(pane, target.path, listingRefresh)
}

func (m *Model) queueUpload() tea.Cmd {
	if !m.connected() {
		m.setError("not connected")
		return nil
	}
	if len(m.local.actionEntries()) == 0 {
		m.setError("nothing selected or highlighted locally")
		return nil
	}
	return m.queueTransfer(domain.Upload)
}

func (m *Model) queueDownload() tea.Cmd {
	if !m.connected() {
		m.setError("not connected")
		return nil
	}
	if len(m.remote.actionEntries()) == 0 {
		m.setError("nothing selected or highlighted remotely")
		return nil
	}
	return m.queueTransfer(domain.Download)
}

func (m *Model) queueFocusedTransfer() tea.Cmd {
	if m.focus == focusRemote {
		return m.queueTransfer(domain.Download)
	}
	return m.queueTransfer(domain.Upload)
}

// queueTransfer queues the focused pane's selection. Plain files queue
// instantly, unchanged from before. Any folder in the selection instead
// starts a preflight scan (beginPreflightScan) covering the whole
// selection — plain files included — so there is one predictable path
// rather than some files queuing immediately while the rest wait on a
// scan.
func (m *Model) queueTransfer(direction domain.TransferDirection) tea.Cmd {
	var entries []domain.Entry
	var srcBase, dstBase string
	var srcFS, dstFS vfs.FS
	var showHidden bool
	if direction == domain.Download {
		entries = m.remote.actionEntries()
		srcBase, srcFS = m.remote.path, m.remoteFS
		dstBase, dstFS = m.local.path, m.localFS
		showHidden = m.remote.showHidden
	} else {
		entries = m.local.actionEntries()
		srcBase, srcFS = m.local.path, m.localFS
		dstBase, dstFS = m.remote.path, m.remoteFS
		showHidden = m.local.showHidden
	}
	if len(entries) == 0 {
		return nil
	}

	hasFolder := false
	for _, entry := range entries {
		if entry.IsDir() {
			hasFolder = true
			break
		}
	}
	if hasFolder {
		return m.beginPreflightScan(direction, entries, srcBase, dstBase, srcFS, dstFS, showHidden)
	}

	queued := 0
	for _, entry := range entries {
		m.transfers = append(m.transfers, domain.Transfer{
			ID:          m.nextTransferID,
			Direction:   direction,
			Source:      srcFS.Child(srcBase, entry.Name),
			Destination: dstFS.Child(dstBase, entry.Name),
			BytesTotal:  entry.Size,
			Status:      domain.Queued,
			Message:     "queued",
		})
		m.nextTransferID++
		queued++
	}
	m.setStatus(fmt.Sprintf("queued %d transfer(s)", queued))
	m.startQueuedTransfers()
	return nil
}

// preflightScanCap bounds how many files one preflight scan will discover
// before giving up and reporting a lower bound instead (scan.truncated),
// so a pathological or enormous tree cannot make queuing a folder hang the
// UI or balloon memory.
const preflightScanCap = 5000

// preflightScanTimeout bounds the whole walk, across every directory it
// has to List, not just one call the way listTimeout bounds a single
// listing.
const preflightScanTimeout = 60 * time.Second

// preflightFile is one file discovered while walking a folder queued for
// transfer — everything queueTransfer needs to turn it into a
// domain.Transfer once the scan is confirmed.
type preflightFile struct {
	src, dst string
	size     int64
}

// preflightScan is the result of walking every folder in a queueTransfer
// selection, held in Model.preflight and shown as overlayPreflight before
// any of it is actually queued.
type preflightScan struct {
	direction  domain.TransferDirection
	files      []preflightFile
	folders    int
	totalBytes int64
	truncated  bool // hit preflightScanCap; files/totalBytes are a lower bound
}

// preflightScanMsg reports beginPreflightScan's result.
type preflightScanMsg struct {
	scan preflightScan
	err  error
}

// beginPreflightScan walks every directory in entries with srcFS.List
// (plain files need no walk — their size is already known from the
// listing) and delivers the result as preflightScanMsg. It is the only
// path a folder is ever queued through: transfer.Engine has no
// folder-copy primitive, so every file it ever moves for a folder queue
// action is flattened into an ordinary domain.Transfer here first, in the
// UI, which is where the queue has always lived (see Design Notes).
// EntrySymlink is queued as a leaf, matching entry.IsDir()'s existing
// semantics elsewhere — never followed, so a symlink cannot turn the walk
// into a cycle.
func (m *Model) beginPreflightScan(direction domain.TransferDirection, entries []domain.Entry, srcBase, dstBase string, srcFS, dstFS vfs.FS, showHidden bool) tea.Cmd {
	m.setStatus("scanning…")
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), preflightScanTimeout)
		defer cancel()

		scan := preflightScan{direction: direction}
		type walkItem struct{ srcPath, dstPath string }
		var stack []walkItem
		for _, entry := range entries {
			srcPath, dstPath := srcFS.Child(srcBase, entry.Name), dstFS.Child(dstBase, entry.Name)
			if entry.IsDir() {
				scan.folders++
				stack = append(stack, walkItem{srcPath, dstPath})
				continue
			}
			scan.files = append(scan.files, preflightFile{src: srcPath, dst: dstPath, size: entry.Size})
			scan.totalBytes += entry.Size
		}

		for len(stack) > 0 {
			if len(scan.files) >= preflightScanCap {
				scan.truncated = true
				break
			}
			item := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			children, err := srcFS.List(ctx, item.srcPath, showHidden)
			if err != nil {
				return preflightScanMsg{err: fmt.Errorf("scan %s: %w", item.srcPath, err)}
			}
			for _, child := range children {
				childSrc, childDst := srcFS.Child(item.srcPath, child.Name), dstFS.Child(item.dstPath, child.Name)
				if child.IsDir() {
					scan.folders++
					stack = append(stack, walkItem{childSrc, childDst})
					continue
				}
				if len(scan.files) >= preflightScanCap {
					scan.truncated = true
					break
				}
				scan.files = append(scan.files, preflightFile{src: childSrc, dst: childDst, size: child.Size})
				scan.totalBytes += child.Size
			}
		}
		if len(stack) > 0 {
			scan.truncated = true
		}
		return preflightScanMsg{scan: scan}
	}
}

// applyPreflightScan folds a completed scan into the model: an error is
// reported directly, an empty result (every selected folder was empty, and
// the selection had no plain files) needs no confirmation and is reported
// directly too, and anything else opens overlayPreflight so the user
// confirms before it is actually queued.
func (m *Model) applyPreflightScan(msg preflightScanMsg) {
	if msg.err != nil {
		m.setError(fmt.Sprintf("scan: %v", msg.err))
		return
	}
	if len(msg.scan.files) == 0 {
		m.setStatus(fmt.Sprintf("%d empty folder(s), nothing to queue", msg.scan.folders))
		return
	}
	scan := msg.scan
	m.preflight = &scan
	m.overlay = overlayPreflight
}

// confirmPreflightQueue queues every file the last preflight scan found and
// clears it. Only reachable from the overlayPreflight confirm key, so
// m.preflight is never nil in practice, but the check keeps it safe
// regardless.
func (m *Model) confirmPreflightQueue() tea.Cmd {
	if m.preflight == nil {
		return nil
	}
	scan := m.preflight
	m.preflight = nil
	for _, file := range scan.files {
		m.transfers = append(m.transfers, domain.Transfer{
			ID:          m.nextTransferID,
			Direction:   scan.direction,
			Source:      file.src,
			Destination: file.dst,
			BytesTotal:  file.size,
			Status:      domain.Queued,
			Message:     "queued",
		})
		m.nextTransferID++
	}
	m.setStatus(fmt.Sprintf("queued %d transfer(s) from %d folder(s)", len(scan.files), scan.folders))
	m.startQueuedTransfers()
	return nil
}

// adjustMaxParallel changes how many transfers run at once, clamped to
// [1, maxParallelCap]. Raising it can immediately start transfers that were
// sitting in Queued waiting for a slot, so it calls startQueuedTransfers;
// lowering it never stops one already Active, since Runner has no pause —
// active transfers stay running until the queue naturally has fewer than the
// new cap.
func (m *Model) adjustMaxParallel(delta int) tea.Cmd {
	m.maxParallel = min(maxParallelCap, max(1, m.maxParallel+delta))
	m.setStatus(fmt.Sprintf("parallel transfers: %d", m.maxParallel))
	m.startQueuedTransfers()
	return m.persist()
}

// startQueuedTransfers promotes queued transfers to active, up to maxParallel,
// handing each one to the engine. Nothing else moves a transfer forward: all
// progress after this point arrives as transfer.Event.
func (m *Model) startQueuedTransfers() {
	if !m.connected() {
		return
	}
	active := countStatus(m.transfers, domain.Active)
	for i := range m.transfers {
		if active >= m.maxParallel {
			return
		}
		if m.transfers[i].Status != domain.Queued {
			continue
		}
		m.transfers[i].Status = domain.Active
		m.transfers[i].StartedAt = time.Now()
		m.transfers[i].Message = "transferring"
		m.engine.Start(transfer.Request{
			ID:          m.transfers[i].ID,
			Direction:   m.transfers[i].Direction,
			Source:      m.transfers[i].Source,
			Destination: m.transfers[i].Destination,
			Size:        m.transfers[i].BytesTotal,
		})
		active++
	}
}

// applyTransferEvent folds one engine event into the queue row it belongs to.
// Events for unknown IDs are ignored: the engine may still be draining a
// transfer the UI has already forgotten.
func (m *Model) applyTransferEvent(event transfer.Event) {
	index := m.transferIndex(event.ID)
	if index < 0 {
		return
	}
	row := &m.transfers[index]
	// Only clamp against a total worth trusting. A listing can report a size
	// of 0 for a file that turns out not to be empty (an FTP LIST parser
	// that could not tell, say); clamping BytesDone to that 0 would freeze
	// the row's progress at 0 for the whole transfer instead of tracking
	// what is actually moving.
	if row.BytesTotal > 0 {
		row.BytesDone = min(event.BytesDone, row.BytesTotal)
	} else {
		row.BytesDone = event.BytesDone
	}
	switch event.Kind {
	case transfer.Progress:
		row.Status = domain.Active
		row.Message = "transferring"
	case transfer.Completed:
		if row.BytesTotal > 0 {
			row.BytesDone = row.BytesTotal
		}
		row.Status = domain.Done
		row.FinishedAt = time.Now()
		row.Message = "complete"
	case transfer.Failed:
		row.Status = domain.Failed
		row.FinishedAt = time.Now()
		row.Message = failureMessage(event.Err)
		m.setError(fmt.Sprintf("transfer %d failed: %s", row.ID, row.Message))
	case transfer.Canceled:
		// Canceled transfers share the Failed tab — there is no tab of their
		// own — but keep their own status distinct from Failed so
		// retrySelectedTransfer, and the user reading the row, can tell a
		// cancellation apart from a real failure.
		row.Status = domain.Canceled
		row.FinishedAt = time.Now()
		row.Message = "canceled"
		m.logs = append(m.logs, fmt.Sprintf("transfer %d canceled", row.ID))
	}
}

// bottomTabFilter reports whether a transfer belongs in the currently
// selected bottom-pane tab. tabQueue deliberately excludes Done: a transfer
// that finishes simply stops matching Queue's filter on its very next
// render and lives on only in tabHistory from then on — that is the whole
// of "aging" a completed transfer out of the live queue, no timer involved.
func (m Model) bottomTabFilter() func(domain.Transfer) bool {
	switch m.bottomTab {
	case tabQueue:
		return func(t domain.Transfer) bool { return t.Status == domain.Queued || t.Status == domain.Active }
	case tabActive:
		return func(t domain.Transfer) bool { return t.Status == domain.Active }
	case tabFailed:
		return func(t domain.Transfer) bool { return t.Status == domain.Failed || t.Status == domain.Canceled }
	case tabHistory:
		return func(t domain.Transfer) bool { return t.Status == domain.Done }
	default:
		return func(domain.Transfer) bool { return false }
	}
}

// bottomTabTransfers returns the transfers belonging to the current
// bottom-pane tab, in queue order — the single source of truth both
// rendering (renderBottomPane, bottomRowCount) and row targeting
// (bottomCursor, cancelActiveTransfers, retrySelectedTransfer) go through,
// so a row's on-screen position and what x/R act on can never disagree.
func (m Model) bottomTabTransfers() []domain.Transfer {
	keep := m.bottomTabFilter()
	rows := make([]domain.Transfer, 0, len(m.transfers))
	for _, t := range m.transfers {
		if keep(t) {
			rows = append(rows, t)
		}
	}
	return rows
}

// cancelActiveTransfers stops transfers. With the queue pane focused on a
// transfer tab (not tabLog, which has no rows), it targets only the row
// under bottomCursor; otherwise — including from a file pane, or from the
// queue pane on tabLog — it is the original all-or-nothing behavior and
// stops everything in flight.
func (m *Model) cancelActiveTransfers() {
	if !m.connected() {
		m.setError("not connected")
		return
	}
	if m.focus == focusQueue && m.bottomTab != tabLog {
		m.cancelTransferAt(m.bottomCursor)
		return
	}
	ids := make([]int, 0, len(m.transfers))
	for _, row := range m.transfers {
		if row.Status == domain.Active {
			ids = append(ids, row.ID)
		}
	}
	if len(ids) == 0 {
		m.setError("no active transfers to cancel")
		return
	}
	for _, id := range ids {
		m.engine.Cancel(id)
	}
	m.setStatus(fmt.Sprintf("cancelling %d transfer(s)", len(ids)))
}

// cancelTransferAt cancels or drops the transfer at index within the
// current tab's rows (see bottomTabTransfers): an Active one is canceled
// through the engine, like the all-active path; a Queued one is simply
// removed, since it was never handed to the engine in the first place and
// there is nothing to cancel. Anything else under the cursor (Done, Failed,
// Canceled) is left alone — x has nothing to do to a row already finished.
func (m *Model) cancelTransferAt(index int) {
	rows := m.bottomTabTransfers()
	if index < 0 || index >= len(rows) {
		m.setError("no transfer selected")
		return
	}
	target := rows[index]
	switch target.Status {
	case domain.Active:
		m.engine.Cancel(target.ID)
		m.setStatus(fmt.Sprintf("cancelling transfer %d", target.ID))
	case domain.Queued:
		if i := m.transferIndex(target.ID); i >= 0 {
			m.transfers = append(m.transfers[:i], m.transfers[i+1:]...)
		}
		m.setStatus(fmt.Sprintf("removed queued transfer %d", target.ID))
	default:
		m.setError("selected transfer is not active or queued")
	}
}

// retrySelectedTransfer re-queues the Failed or Canceled transfer under
// bottomCursor as a brand new Transfer — fresh ID, zeroed progress — rather
// than mutating the original row in place, so the original stays in the
// Failed tab as a record of what happened while the retry runs its own
// course as a normal queued transfer.
func (m *Model) retrySelectedTransfer() tea.Cmd {
	if !m.connected() {
		m.setError("not connected")
		return nil
	}
	if m.focus != focusQueue || m.bottomTab == tabLog {
		m.setError("select a failed transfer to retry")
		return nil
	}
	rows := m.bottomTabTransfers()
	if m.bottomCursor < 0 || m.bottomCursor >= len(rows) {
		m.setError("no transfer selected")
		return nil
	}
	target := rows[m.bottomCursor]
	if target.Status != domain.Failed && target.Status != domain.Canceled {
		m.setError("selected transfer did not fail")
		return nil
	}
	m.transfers = append(m.transfers, domain.Transfer{
		ID:          m.nextTransferID,
		Direction:   target.Direction,
		Source:      target.Source,
		Destination: target.Destination,
		BytesTotal:  target.BytesTotal,
		Status:      domain.Queued,
		Message:     "queued",
	})
	m.nextTransferID++
	m.setStatus(fmt.Sprintf("retrying transfer %d as %d", target.ID, m.nextTransferID-1))
	m.startQueuedTransfers()
	return nil
}

func (m Model) transferIndex(id int) int {
	for i := range m.transfers {
		if m.transfers[i].ID == id {
			return i
		}
	}
	return -1
}

func failureMessage(err error) string {
	if err == nil {
		return "failed"
	}
	return err.Error()
}

func (m *Model) focusedFilePane() *filePane {
	switch m.focus {
	case focusLocal:
		return &m.local
	case focusRemote:
		return &m.remote
	default:
		return nil
	}
}

// clampCursors keeps every cursor — both file panes' and the transfers
// pane's row cursor — within bounds after whatever the last key did. It
// runs after every keypress, not just navigation, since actions like
// dropping a queued transfer or switching tabs can shrink the list a
// cursor was pointing into.
func (m *Model) clampCursors() {
	visible := m.filePaneVisibleRows()
	m.local.clamp(visible)
	m.remote.clamp(visible)
	m.clampBottomCursor()
}

// clampBottomCursor keeps bottomCursor inside the current tab's row count
// and scrolls bottomOffset to follow it into view — the same shape as
// filePane.clamp, over bottomTabTransfers() instead of a filePane's
// entries. tabLog has no rows to select, and owns bottomOffset itself
// (settleBottomOffset/clampBottomOffset, driven by bottomRowCount), so this
// leaves it alone entirely.
func (m *Model) clampBottomCursor() {
	if m.bottomTab == tabLog {
		return
	}
	rows := len(m.bottomTabTransfers())
	visible := max(1, m.bottomVisibleRows())
	m.bottomCursor = min(max(0, m.bottomCursor), max(0, rows-1))
	if m.bottomCursor < m.bottomOffset {
		m.bottomOffset = m.bottomCursor
	}
	if m.bottomCursor >= m.bottomOffset+visible {
		m.bottomOffset = m.bottomCursor - visible + 1
	}
	m.bottomOffset = min(m.bottomOffset, max(0, rows-visible))
	m.bottomOffset = max(0, m.bottomOffset)
}

// setBottomTab switches the focused bottom-pane tab and resets its scroll
// position and row cursor. The log tab opens scrolled to the latest
// entries, matching a tail view; the transfer tabs open scrolled to the top.
func (m *Model) setBottomTab(tab bottomTab) {
	m.bottomTab = tab
	m.bottomCursor = 0
	if tab == tabLog {
		m.bottomOffset = max(0, len(m.logs)-m.bottomVisibleRows())
	} else {
		m.bottomOffset = 0
	}
}

func (m *Model) clampBottomOffset() {
	m.bottomOffset = min(m.bottomOffset, max(0, m.bottomRowCount()-m.bottomVisibleRows()))
	m.bottomOffset = max(0, m.bottomOffset)
}

// isAtBottomPane reports whether the bottom pane is currently scrolled all
// the way down for its tab, i.e. showing the latest rows.
func (m Model) isAtBottomPane() bool {
	return m.bottomOffset >= max(0, m.bottomRowCount()-m.bottomVisibleRows())
}

// settleBottomOffset re-clamps the scroll offset after the row count for the
// current tab may have changed. If the pane was already scrolled to the
// bottom, it stays pinned there (auto-follow) so new transfer activity or
// log lines stay visible; otherwise it's just clamped to the valid range.
func (m *Model) settleBottomOffset(wasAtBottom bool) {
	if wasAtBottom {
		m.bottomOffset = max(0, m.bottomRowCount()-m.bottomVisibleRows())
		return
	}
	m.clampBottomOffset()
}

func (m *Model) cursorFromMouse(pane *filePane, y int) {
	row := y - firstFileRow
	if row < 0 {
		return
	}
	pane.cursor = pane.offset + row
	pane.clamp(m.filePaneVisibleRows())
}

func (m *Model) setStatus(value string) {
	m.status, m.statusErr = value, false
}

func (m *Model) setError(value string) {
	m.status, m.statusErr = value, true
	m.logs = append(m.logs, "error: "+value)
}

func (p *filePane) current() (domain.Entry, bool) {
	if p.cursor < 0 || p.cursor >= len(p.entries) {
		return domain.Entry{}, false
	}
	return p.entries[p.cursor], true
}

// clamp keeps the cursor inside the entry list and scrolls the offset so the
// cursor stays within the visible window, where visible is how many entry rows
// the pane can actually draw at the current terminal size.
func (p *filePane) clamp(visible int) {
	visible = max(1, visible)
	p.cursor = min(max(0, p.cursor), max(0, len(p.entries)-1))
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+visible {
		p.offset = p.cursor - visible + 1
	}
	p.offset = min(p.offset, max(0, len(p.entries)-visible))
	p.offset = max(0, p.offset)
}

// reset returns a pane to the top of a freshly entered directory and drops
// the previous directory's selection.
func (p *filePane) reset() {
	p.cursor, p.offset = 0, 0
	p.selected = map[string]bool{}
}

func (p filePane) actionEntries() []domain.Entry {
	if len(p.selected) == 0 {
		entry, ok := p.current()
		if !ok {
			return nil
		}
		return []domain.Entry{entry}
	}
	entries := make([]domain.Entry, 0, len(p.selected))
	for _, entry := range p.entries {
		if p.selected[entry.Name] {
			entries = append(entries, entry)
		}
	}
	return entries
}

// waitForTransferEvent blocks a command goroutine on the engine's event
// channel and delivers the next event as a message. Update re-issues it after
// each event, which is the standard Bubble Tea way to pump an external stream.
func waitForTransferEvent(events <-chan transfer.Event) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return transferStreamClosed{}
		}
		return event
	}
}

func short(value string, width int) string {
	return ansi.Truncate(value, max(1, width), "…")
}
