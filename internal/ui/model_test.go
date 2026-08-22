package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
	"tideftp/internal/localfs"
	"tideftp/internal/session"
	"tideftp/internal/transfer"
	"tideftp/internal/vfs"
)

// testTarget is the profile the test dialer answers for.
var testTarget = session.Target{Name: "test", Protocol: "sftp", Host: "test.local", User: "allie", StartPath: "/public_html"}

// stubConn is a session.Conn over adapters the test supplies, so UI tests can
// drive a connection without any real dialing. drop ends it as a server going
// away would.
type stubConn struct {
	fs     vfs.FS
	engine transfer.Engine
	done   chan error
	once   sync.Once
	closed bool
}

func (c *stubConn) FS() vfs.FS              { return c.fs }
func (c *stubConn) Engine() transfer.Engine { return c.engine }
func (c *stubConn) Done() <-chan error      { return c.done }

func (c *stubConn) Close() error {
	c.closed = true
	c.end(nil)
	return nil
}

func (c *stubConn) drop(err error) { c.end(err) }

func (c *stubConn) end(reason error) {
	c.once.Do(func() {
		c.done <- reason
		close(c.done)
	})
}

// stubDialer hands out stubConns, or fails with err when one is set.
type stubDialer struct {
	fs     vfs.FS
	engine transfer.Engine
	err    error
	calls  []session.Target
	conns  []*stubConn
}

func (d *stubDialer) Dial(_ context.Context, target session.Target) (session.Conn, error) {
	d.calls = append(d.calls, target)
	if d.err != nil {
		return nil, d.err
	}
	conn := &stubConn{fs: d.fs, engine: d.engine, done: make(chan error, 1)}
	d.conns = append(d.conns, conn)
	return conn, nil
}

// loadedModel builds a connected model over the fake adapters and settles its
// listings, so a test can treat both panes as loaded.
func loadedModel(t *testing.T, engine transfer.Engine) Model {
	t.Helper()
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: engine})
	return model
}

func loadedModelOver(t *testing.T, local, remote vfs.FS, engine transfer.Engine) Model {
	t.Helper()
	model, _ := connectModel(t, local, &stubDialer{fs: remote, engine: engine})
	return model
}

func loadedModelWithDialer(t *testing.T, dialer *stubDialer) (Model, *stubDialer) {
	t.Helper()
	return connectModel(t, localfs.New(), dialer)
}

// connectModel wires a live connection into a model without going through
// applyConnected.
//
// applyConnected returns the transfer event pump and the connection watcher,
// and neither ever returns on its own. settle abandons a parked command but
// cannot stop the goroutine running it, and a parked pump goes on reading the
// engine's channel — stealing events from any test that owns that stream. So
// the connection is wired in directly here. The real connect path is covered
// by TestInitLoadsLocalAndDialsTheFirstTarget, and disconnection by feeding
// disconnectedMsg in by hand.
func connectModel(t *testing.T, local vfs.FS, dialer *stubDialer) (Model, *stubDialer) {
	t.Helper()
	model := NewModel(local, dialer, []session.Target{testTarget})
	model.width, model.height = 120, 36
	model = settle(t, model, listCmd(model.localFS, paneLocal, model.local.requestToken, model.local.path, model.local.showHidden, listingNavigate))
	if dialer.err != nil {
		return model, dialer
	}

	conn, err := dialer.Dial(context.Background(), testTarget)
	if err != nil {
		t.Fatalf("stub dial: %v", err)
	}
	model.target = testTarget
	model.conn = conn
	model.remoteFS = conn.FS()
	model.engine = conn.Engine()
	model.state = connConnected
	model = settle(t, model, model.requestListing(paneRemote, testTarget.Home(), listingNavigate))
	if !model.connected() {
		t.Fatalf("test model did not connect: state=%v err=%v", model.state, model.connErr)
	}
	return model, dialer
}

// settleGrace is how long settle waits for one command before treating it as
// parked. The fakes answer instantly, so anything slower is the transfer event
// pump or the connection watcher, which by design never return on their own.
const settleGrace = 50 * time.Millisecond

// settle runs cmd and everything it produces against the model until nothing
// is left, so tests can treat asynchronous work as if it were synchronous.
// Commands that park are skipped; tests drive those paths by hand.
func settle(t *testing.T, model Model, cmd tea.Cmd) Model {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 200 {
			t.Fatalf("commands never settled")
		}
		next := queue[0]
		queue = queue[1:]
		if next == nil {
			continue
		}
		result := make(chan tea.Msg, 1)
		go func() { result <- next() }()

		var msg tea.Msg
		select {
		case msg = <-result:
		case <-time.After(settleGrace):
			continue
		}

		switch typed := msg.(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, typed...)
		default:
			updated, follow := model.Update(msg)
			model = updated.(Model)
			queue = append(queue, follow)
		}
	}
	return model
}

// press sends one key and settles whatever it kicks off.
func press(t *testing.T, model Model, key tea.KeyMsg) Model {
	t.Helper()
	updated, cmd := model.updateKey(key)
	return settle(t, updated.(Model), cmd)
}

func runes(value string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
}

// scriptedEngine is a transfer.Engine the tests drive by hand: nothing runs on
// a timer and no goroutine is involved, so UI tests assert on engine traffic
// without depending on scheduling. faketransfer has its own tests for the
// simulated timing behaviour.
type scriptedEngine struct {
	events   chan transfer.Event
	started  []transfer.Request
	canceled []int
	closed   bool
}

func newScriptedEngine() *scriptedEngine {
	return &scriptedEngine{events: make(chan transfer.Event, 64)}
}

func (e *scriptedEngine) Start(req transfer.Request)    { e.started = append(e.started, req) }
func (e *scriptedEngine) Cancel(id int)                 { e.canceled = append(e.canceled, id) }
func (e *scriptedEngine) Events() <-chan transfer.Event { return e.events }
func (e *scriptedEngine) Close() error                  { e.closed = true; return nil }

func (e *scriptedEngine) startedIDs() []int {
	ids := make([]int, 0, len(e.started))
	for _, req := range e.started {
		ids = append(ids, req.ID)
	}
	return ids
}

func TestShiftArrowsResizeLayout(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	startFile := model.fileSplit.Value()
	updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyShiftRight})
	model = updated.(Model)
	if model.fileSplit.Value() <= startFile {
		t.Fatalf("shift+right did not grow file split: got %v want > %v", model.fileSplit.Value(), startFile)
	}
	startBottom := model.bottomSplit.Value()
	updated, _ = model.updateKey(tea.KeyMsg{Type: tea.KeyShiftUp})
	model = updated.(Model)
	if model.bottomSplit.Value() <= startBottom {
		t.Fatalf("shift+up did not grow bottom split: got %v want > %v", model.bottomSplit.Value(), startBottom)
	}
}

func TestThemePickerIncludesTideNight(t *testing.T) {
	themes := appThemes()
	if len(themes) == 0 || themes[0].Name != "tide-night" {
		t.Fatalf("first theme = %q, want tide-night", themes[0].Name)
	}
}

func TestBottomPaneScrolls(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.width, model.height = 100, 30
	model.focus = focusQueue
	for i := 0; i < 40; i++ {
		model.transfers = append(model.transfers, domain.Transfer{ID: i, Status: domain.Queued})
	}
	visible := model.bottomVisibleRows()
	if visible <= 0 || visible >= len(model.transfers) {
		t.Fatalf("expected a visible row count smaller than the transfer count, got %d visible of %d", visible, len(model.transfers))
	}

	updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(Model)
	if model.bottomOffset != 1 {
		t.Fatalf("bottomOffset after one down = %d, want 1", model.bottomOffset)
	}

	for i := 0; i < 100; i++ {
		updated, _ = model.updateKey(tea.KeyMsg{Type: tea.KeyDown})
		model = updated.(Model)
	}
	wantMax := len(model.transfers) - visible
	if model.bottomOffset != wantMax {
		t.Fatalf("bottomOffset after many downs = %d, want clamped to %d", model.bottomOffset, wantMax)
	}

	updated, _ = model.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	model = updated.(Model)
	if model.bottomOffset != 0 {
		t.Fatalf("switching tabs should reset bottomOffset, got %d", model.bottomOffset)
	}
}

func TestLogTabOpensScrolledToLatest(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.width, model.height = 100, 30
	model.focus = focusQueue
	for i := 0; i < 50; i++ {
		model.logs = append(model.logs, "line")
	}

	updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")})
	model = updated.(Model)
	want := len(model.logs) - model.bottomVisibleRows()
	if model.bottomOffset != want {
		t.Fatalf("log tab bottomOffset = %d, want %d (scrolled to latest)", model.bottomOffset, want)
	}
}

func TestBottomPaneAutoFollowsWhenAlreadyAtBottom(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.width, model.height = 100, 30
	model.local.entries = []domain.Entry{{Name: "a"}}
	model.local.cursor = 0
	model.focus = focusQueue
	model.bottomTab = tabQueue
	for i := 0; i < 30; i++ {
		model.transfers = append(model.transfers, domain.Transfer{ID: i, Status: domain.Queued})
	}
	visible := model.bottomVisibleRows()

	for i := 0; i < 100; i++ {
		updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyDown})
		model = updated.(Model)
	}
	if !model.isAtBottomPane() {
		t.Fatalf("expected to be scrolled to the bottom before new activity arrives")
	}

	// "u" queues a new transfer inside the same updateKey call whose
	// wasAtBottom snapshot precedes it, exactly like real usage (a
	// transferTick or queued transfer growing the list mid-update).
	updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
	model = updated.(Model)
	if len(model.transfers) != 31 {
		t.Fatalf("expected queueUpload to add exactly one transfer, got %d total", len(model.transfers))
	}

	wantOffset := len(model.transfers) - visible
	if model.bottomOffset != wantOffset {
		t.Fatalf("bottomOffset after queuing a transfer while at bottom = %d, want %d (auto-follow)", model.bottomOffset, wantOffset)
	}
}

func TestBottomPaneStaysPutWhenScrolledUpDuringActivity(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.width, model.height = 100, 30
	model.focus = focusQueue
	for i := 0; i < 30; i++ {
		model.transfers = append(model.transfers, domain.Transfer{ID: i, Status: domain.Queued})
	}

	updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(Model)
	if model.bottomOffset != 1 {
		t.Fatalf("bottomOffset after one down = %d, want 1", model.bottomOffset)
	}
	if model.isAtBottomPane() {
		t.Fatalf("expected to not be at the bottom after scrolling only one row into a long list")
	}

	next, _ := model.Update(transfer.Event{ID: 5, Kind: transfer.Progress, BytesDone: 10})
	model = next.(Model)
	if model.bottomOffset != 1 {
		t.Fatalf("bottomOffset after engine activity while scrolled up = %d, want unchanged at 1", model.bottomOffset)
	}
}

func TestManualScrollUpIsNotOverriddenByFollow(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.width, model.height = 100, 30
	model.focus = focusQueue
	for i := 0; i < 30; i++ {
		model.transfers = append(model.transfers, domain.Transfer{ID: i, Status: domain.Queued})
	}
	for i := 0; i < 100; i++ {
		updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyDown})
		model = updated.(Model)
	}
	atBottom := model.bottomOffset

	updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyUp})
	model = updated.(Model)
	if model.bottomOffset != atBottom-1 {
		t.Fatalf("scrolling up from the bottom = %d, want %d (not snapped back by auto-follow)", model.bottomOffset, atBottom-1)
	}
}

func TestIconsToggleAndAsciiFallback(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	if !model.showIcons {
		t.Fatalf("icons should default on")
	}

	updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	model = updated.(Model)
	if model.showIcons {
		t.Fatalf("pressing i should turn icons off")
	}
	renderer := tideui.NewRenderer(model.theme, tideui.StyleOptions{})
	if got := model.entryIcon(renderer, domain.Entry{Kind: domain.EntryDir}); got != ">" {
		t.Fatalf("dir icon with icons off = %q, want ascii fallback %q", got, ">")
	}

	updated, _ = model.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	model = updated.(Model)
	if !model.showIcons {
		t.Fatalf("pressing i again should turn icons back on")
	}
	renderer = tideui.NewRenderer(model.theme, tideui.StyleOptions{})
	if got := model.entryIcon(renderer, domain.Entry{Kind: domain.EntryDir}); got != "▸" {
		t.Fatalf("dir icon with icons on = %q, want unicode glyph", got)
	}

	// An ASCII-presentation theme (vt52) should fall back to ASCII glyphs
	// even when the icon toggle itself is on.
	model.theme = tideui.VT52
	renderer = tideui.NewRenderer(model.theme, tideui.StyleOptions{})
	if got := model.entryIcon(renderer, domain.Entry{Kind: domain.EntryDir}); got != ">" {
		t.Fatalf("dir icon under an ASCII theme = %q, want ascii fallback %q", got, ">")
	}
}

func TestViewContainsThreeOperationalRegions(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.width = 120
	model.height = 36
	view := model.View()
	for _, want := range []string{"Local", "Remote", "Transfers", "Queue", "Active", "Failed", "History", "Log"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q\n%s", want, view)
		}
	}
}

func TestMouseClickSelectsTheClickedRow(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.width, model.height = 120, 36
	model.local.entries = []domain.Entry{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}}
	model.local.cursor, model.local.offset = 0, 0

	click := func(y int) Model {
		next, _ := model.updateMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: y})
		return next.(Model)
	}

	// firstFileRow is the first entry; anything above it is chrome and must
	// not move the cursor.
	got := click(firstFileRow)
	if got.focus != focusLocal || got.local.cursor != 0 {
		t.Fatalf("click on the first entry row: focus=%v cursor=%d, want local pane cursor 0", got.focus, got.local.cursor)
	}
	if got := click(firstFileRow + 2); got.local.cursor != 2 {
		t.Fatalf("click two rows down = cursor %d, want 2", got.local.cursor)
	}

	model.local.cursor = 3
	if got := click(firstFileRow - 1); got.local.cursor != 3 {
		t.Fatalf("click on the column header moved the cursor to %d, want it left at 3", got.local.cursor)
	}
}

func TestMouseClickBelowTopPaneFocusesQueue(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.width, model.height = 120, 36
	// The row right after the top panes belongs to the transfers pane, using
	// the same height View draws with.
	next, _ := model.updateMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: 1 + model.topPaneHeight()})
	if got := next.(Model).focus; got != focusQueue {
		t.Fatalf("click below the top panes focused %v, want focusQueue", got)
	}
}

func TestEnteringDirectoryClearsSelection(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/"))

	// Select every entry in the root, then walk into a subdirectory.
	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlA})
	if len(model.remote.selected) == 0 {
		t.Fatalf("expected ctrl+a to select the root entries")
	}
	for index, entry := range model.remote.entries {
		if entry.IsDir() {
			model.remote.cursor = index
			break
		}
	}
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if model.remote.path == "/" {
		t.Fatalf("enter did not move into a subdirectory")
	}
	if len(model.remote.selected) != 0 {
		t.Fatalf("selection survived a directory change: %v", model.remote.selected)
	}
	if model.remote.cursor != 0 || model.remote.offset != 0 {
		t.Fatalf("entering a directory left cursor=%d offset=%d, want 0/0", model.remote.cursor, model.remote.offset)
	}

	// Going back up is a directory change too.
	model.remote.selected["public_html"] = true
	model = press(t, model, tea.KeyMsg{Type: tea.KeyBackspace})
	if model.remote.path != "/" {
		t.Fatalf("backspace left the pane at %q, want /", model.remote.path)
	}
	if len(model.remote.selected) != 0 {
		t.Fatalf("selection survived a move to the parent directory: %v", model.remote.selected)
	}
}

func TestFilePaneScrollsWithTheVisibleHeight(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.width, model.height = 120, 36
	model.focus = focusLocal
	entries := make([]domain.Entry, 60)
	for i := range entries {
		entries[i] = domain.Entry{Name: string(rune('a' + i%26))}
	}
	model.local.entries = entries
	model.local.cursor, model.local.offset = 0, 0

	visible := model.filePaneVisibleRows()
	if visible <= 0 || visible >= len(entries) {
		t.Fatalf("visible rows = %d, want a window smaller than the %d entries", visible, len(entries))
	}

	// Walking to the last row the window can show must not scroll yet.
	for i := 0; i < visible-1; i++ {
		updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyDown})
		model = updated.(Model)
	}
	if model.local.offset != 0 {
		t.Fatalf("offset = %d before the cursor leaves the window, want 0", model.local.offset)
	}

	// One more step scrolls by exactly one row.
	updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(Model)
	if model.local.offset != 1 {
		t.Fatalf("offset after the cursor leaves the window = %d, want 1", model.local.offset)
	}

	// The cursor must stay inside the drawn window at every point.
	for i := 0; i < len(entries)+10; i++ {
		updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyDown})
		model = updated.(Model)
		if model.local.cursor < model.local.offset || model.local.cursor >= model.local.offset+visible {
			t.Fatalf("cursor %d outside the window [%d,%d)", model.local.cursor, model.local.offset, model.local.offset+visible)
		}
	}
	if want := len(entries) - visible; model.local.offset != want {
		t.Fatalf("offset at the end of the list = %d, want %d", model.local.offset, want)
	}
}

func TestParallelismIsCappedByMaxParallel(t *testing.T) {
	engine := newScriptedEngine()
	model := loadedModel(t, engine)
	model.maxParallel = 3
	for i := 0; i < 10; i++ {
		model.transfers = append(model.transfers, domain.Transfer{ID: i + 1, BytesTotal: 10_000_000, Status: domain.Queued})
	}
	model.startQueuedTransfers()

	if got := countStatus(model.transfers, domain.Active); got != 3 {
		t.Fatalf("active transfers = %d, want maxParallel of 3", got)
	}
	if got := engine.startedIDs(); len(got) != 3 {
		t.Fatalf("engine started %v, want exactly 3 requests", got)
	}
}

func TestQueuingHandsTheRequestToTheEngine(t *testing.T) {
	engine := newScriptedEngine()
	model := loadedModel(t, engine)
	model.focus = focusLocal
	model.local.path = "/tmp"
	model.remote.path = "/public_html"
	model.local.entries = []domain.Entry{{Name: "report.pdf", Size: 500_000}}
	model.local.cursor = 0

	updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
	model = updated.(Model)

	if len(engine.started) != 1 {
		t.Fatalf("engine received %d requests, want 1", len(engine.started))
	}
	req := engine.started[0]
	if req.Source != "/tmp/report.pdf" || req.Destination != "/public_html/report.pdf" {
		t.Fatalf("request paths = %q -> %q", req.Source, req.Destination)
	}
	if req.Size != 500_000 || req.Direction != domain.Upload {
		t.Fatalf("request size/direction = %d/%v, want 500000/Upload", req.Size, req.Direction)
	}
	if got := model.transfers[0].Status; got != domain.Active {
		t.Fatalf("queued transfer status = %v, want Active once handed to the engine", got)
	}
}

func TestEngineEventsDriveTransferState(t *testing.T) {
	engine := newScriptedEngine()
	model := loadedModel(t, engine)
	model.width, model.height = 100, 30
	model.transfers = []domain.Transfer{
		{ID: 1, BytesTotal: 1000, Status: domain.Queued},
		{ID: 2, BytesTotal: 1000, Status: domain.Queued},
	}

	apply := func(event transfer.Event) {
		next, _ := model.Update(event)
		model = next.(Model)
	}

	apply(transfer.Event{ID: 1, Kind: transfer.Progress, BytesDone: 400})
	if got := model.transfers[0]; got.Status != domain.Active || got.BytesDone != 400 {
		t.Fatalf("after progress: status=%v bytes=%d, want Active/400", got.Status, got.BytesDone)
	}

	apply(transfer.Event{ID: 1, Kind: transfer.Completed, BytesDone: 1000})
	if got := model.transfers[0]; got.Status != domain.Done || got.BytesDone != got.BytesTotal {
		t.Fatalf("after completion: status=%v bytes=%d, want Done/1000", got.Status, got.BytesDone)
	}

	apply(transfer.Event{ID: 2, Kind: transfer.Failed, BytesDone: 600, Err: errors.New("connection reset by peer")})
	failed := model.transfers[1]
	if failed.Status != domain.Failed {
		t.Fatalf("after failure: status=%v, want Failed", failed.Status)
	}
	if failed.Message != "connection reset by peer" {
		t.Fatalf("failure message = %q, want the engine's error", failed.Message)
	}

	model.bottomTab = tabFailed
	if got := model.bottomRowCount(); got != 1 {
		t.Fatalf("failed tab row count = %d, want 1", got)
	}
	if view := model.View(); !strings.Contains(view, "connection reset by peer") {
		t.Fatalf("failed tab should render the engine's message\n%s", view)
	}

	// An event for a transfer the UI does not know about must be ignored.
	apply(transfer.Event{ID: 999, Kind: transfer.Completed, BytesDone: 1})
	if len(model.transfers) != 2 {
		t.Fatalf("an unknown event changed the queue: %d rows", len(model.transfers))
	}
}

func TestFinishedTransferFreesASlotForTheNextOne(t *testing.T) {
	engine := newScriptedEngine()
	model := loadedModel(t, engine)
	model.maxParallel = 2
	model.transfers = []domain.Transfer{
		{ID: 1, BytesTotal: 1000, Status: domain.Queued},
		{ID: 2, BytesTotal: 1000, Status: domain.Queued},
		{ID: 3, BytesTotal: 1000, Status: domain.Queued},
	}
	model.startQueuedTransfers()
	if got := engine.startedIDs(); len(got) != 2 {
		t.Fatalf("engine started %v, want the first 2 only", got)
	}

	next, _ := model.Update(transfer.Event{ID: 1, Kind: transfer.Completed, BytesDone: 1000})
	model = next.(Model)

	if got := engine.startedIDs(); len(got) != 3 || got[2] != 3 {
		t.Fatalf("engine started %v, want transfer 3 to take the freed slot", got)
	}
	if got := countStatus(model.transfers, domain.Active); got != 2 {
		t.Fatalf("active transfers = %d, want the cap to stay at 2", got)
	}
}

func TestCancelStopsActiveTransfers(t *testing.T) {
	engine := newScriptedEngine()
	model := loadedModel(t, engine)
	model.transfers = []domain.Transfer{
		{ID: 1, BytesTotal: 1000, Status: domain.Active},
		{ID: 2, BytesTotal: 1000, Status: domain.Queued},
	}

	updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	model = updated.(Model)
	if len(engine.canceled) != 1 || engine.canceled[0] != 1 {
		t.Fatalf("engine.Cancel calls = %v, want just the active transfer 1", engine.canceled)
	}

	// The row does not change until the engine confirms.
	if got := model.transfers[0].Status; got != domain.Active {
		t.Fatalf("status right after requesting cancel = %v, want still Active", got)
	}
	next, _ := model.Update(transfer.Event{ID: 1, Kind: transfer.Canceled, BytesDone: 400})
	model = next.(Model)
	if got := model.transfers[0]; got.Status != domain.Failed || got.Message != "canceled" {
		t.Fatalf("after the cancel event: status=%v message=%q, want Failed/canceled", got.Status, got.Message)
	}
}

func TestQuitSchedulesAConnectionShutdown(t *testing.T) {
	model, dialer := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})

	_, cmd := model.updateKey(runes("q"))
	if cmd == nil {
		t.Fatalf("quit produced no command, so nothing shuts the connection down")
	}
	// tea.Sequence hands its commands to the runtime rather than running them,
	// so the sequence itself cannot be executed here. Check the shutdown
	// command it carries does close the connection.
	if msg := closeConnCmd(model.conn)(); msg != nil {
		t.Fatalf("closeConnCmd returned %v, want no message", msg)
	}
	if !dialer.conns[0].closed {
		t.Fatalf("quitting did not close the connection")
	}
}

func TestClosedEventStreamStopsThePump(t *testing.T) {
	engine := newScriptedEngine()
	model := loadedModel(t, engine)
	close(engine.events)

	msg := waitForTransferEvent(engine.Events())()
	if _, ok := msg.(transferStreamClosed); !ok {
		t.Fatalf("a closed event channel produced %T, want transferStreamClosed", msg)
	}
	if _, cmd := model.Update(transferStreamClosed{}); cmd != nil {
		t.Fatalf("the UI should stop pumping once the stream is closed")
	}
}

func TestInitLoadsLocalAndDialsTheFirstTarget(t *testing.T) {
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, []session.Target{testTarget})
	model.width, model.height = 120, 36

	if !model.local.loading {
		t.Fatalf("the local pane should start in its loading state")
	}
	if model.state != connConnecting {
		t.Fatalf("state = %v before Init, want connecting", model.state)
	}
	if model.connected() {
		t.Fatalf("a constructor cannot dial; the model must not report connected")
	}

	model = settle(t, model, model.Init())

	if model.local.loading || len(model.local.entries) == 0 {
		t.Fatalf("local pane did not load: loading=%v entries=%d", model.local.loading, len(model.local.entries))
	}
	if len(dialer.calls) != 1 || dialer.calls[0] != testTarget {
		t.Fatalf("dialer calls = %v, want one for the first target", dialer.calls)
	}
	if !model.connected() {
		t.Fatalf("state = %v after Init, want connected", model.state)
	}
	// The remote pane opens on the target's start path, not on whatever the
	// previous connection was showing.
	if model.remote.path != testTarget.StartPath {
		t.Fatalf("remote pane opened at %q, want the target's start path %q", model.remote.path, testTarget.StartPath)
	}
	if len(model.remote.entries) == 0 {
		t.Fatalf("remote pane has no entries after connecting")
	}
}

func TestModelWithNoTargetsStartsDisconnected(t *testing.T) {
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, nil)
	model.width, model.height = 120, 36
	model = settle(t, model, model.Init())

	if model.state != connDisconnected || model.connected() {
		t.Fatalf("state = %v, want disconnected", model.state)
	}
	if len(dialer.calls) != 0 {
		t.Fatalf("dialed %v with no targets configured", dialer.calls)
	}
	if len(model.local.entries) == 0 {
		t.Fatalf("the local pane must work while disconnected")
	}
}

func TestPaneStaysUsableWhileLoading(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusRemote
	before := append([]domain.Entry(nil), model.remote.entries...)

	cmd := model.navigateTo(paneRemote, "/releases")

	if !model.remote.loading {
		t.Fatalf("pane should be marked loading while the listing is in flight")
	}
	if model.remote.displayPath() != "/releases" {
		t.Fatalf("header shows %q while loading, want the directory being opened", model.remote.displayPath())
	}
	if model.remote.path == "/releases" {
		t.Fatalf("path must not be committed until the listing succeeds")
	}
	if len(model.remote.entries) != len(before) {
		t.Fatalf("pane contents changed before the listing landed")
	}
	if view := model.View(); !strings.Contains(view, "/releases") {
		t.Fatalf("the loading directory should appear in the pane header\n%s", view)
	}

	model = settle(t, model, cmd)

	if model.remote.loading || model.remote.path != "/releases" {
		t.Fatalf("after the listing landed: loading=%v path=%q", model.remote.loading, model.remote.path)
	}
	if model.remote.displayPath() != "/releases" {
		t.Fatalf("displayPath = %q after loading", model.remote.displayPath())
	}
}

func TestStaleListingIsIgnored(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusRemote

	// Two navigations in flight at once, as two quick Enter presses would do.
	slow := model.navigateTo(paneRemote, "/releases")
	fast := model.navigateTo(paneRemote, "/incoming")

	// The newer request answers first.
	model = settle(t, model, fast)
	if model.remote.path != "/incoming" {
		t.Fatalf("newest listing did not land: path = %q", model.remote.path)
	}
	landed := append([]domain.Entry(nil), model.remote.entries...)

	// The older reply arrives late and must be dropped.
	model = settle(t, model, slow)
	if model.remote.path != "/incoming" {
		t.Fatalf("a stale listing overwrote the newer directory: path = %q", model.remote.path)
	}
	if len(model.remote.entries) != len(landed) {
		t.Fatalf("a stale listing replaced the newer entries")
	}
	if model.remote.loading {
		t.Fatalf("a stale reply should not put the pane back into loading")
	}
}

func TestFailedListingLeavesThePaneWhereItWas(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/public_html"))

	pathBefore := model.remote.path
	entriesBefore := len(model.remote.entries)

	model = settle(t, model, model.navigateTo(paneRemote, "/does-not-exist"))

	if model.remote.path != pathBefore {
		t.Fatalf("a failed listing moved the pane to %q, want it left at %q", model.remote.path, pathBefore)
	}
	if len(model.remote.entries) != entriesBefore {
		t.Fatalf("a failed listing changed the pane contents")
	}
	if model.remote.loading {
		t.Fatalf("pane stuck in its loading state after a failed listing")
	}
	if !model.statusErr {
		t.Fatalf("a failed listing should report an error, status = %q", model.status)
	}
	if !strings.Contains(model.status, "remote") {
		t.Fatalf("error status = %q, want it to name the pane", model.status)
	}
}

func TestRefreshKeepsCursorAndSelection(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/public_html"))

	model.remote.cursor = 2
	model.remote.selected[model.remote.entries[2].Name] = true

	model = press(t, model, runes("r"))

	if model.remote.cursor != 2 {
		t.Fatalf("refresh moved the cursor to %d, want it left at 2", model.remote.cursor)
	}
	if len(model.remote.selected) != 1 {
		t.Fatalf("refresh dropped the selection: %v", model.remote.selected)
	}
	if model.remote.path != "/public_html" {
		t.Fatalf("refresh changed the directory to %q", model.remote.path)
	}
}

func TestToggleHiddenRelistsOnlyTheFocusedPane(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/"))

	visible := len(model.remote.entries)
	localToken := model.local.requestToken

	model = press(t, model, runes("."))

	if !model.remote.showHidden {
		t.Fatalf("'.' did not turn hidden files on for the focused pane")
	}
	if len(model.remote.entries) <= visible {
		t.Fatalf("hidden entries = %d, want more than the %d visible ones", len(model.remote.entries), visible)
	}
	if model.local.requestToken != localToken {
		t.Fatalf("toggling hidden files re-listed the unfocused pane too")
	}
	if model.local.showHidden {
		t.Fatalf("toggling hidden files changed the unfocused pane's setting")
	}
}

func TestParentAtRootDoesNothing(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/"))

	token := model.remote.requestToken
	updated, cmd := model.updateKey(tea.KeyMsg{Type: tea.KeyBackspace})
	model = updated.(Model)

	if cmd != nil {
		t.Fatalf("backspace at the root issued a listing")
	}
	if model.remote.requestToken != token || model.remote.loading {
		t.Fatalf("backspace at the root started a request")
	}
}

func TestEnterOnAFileDoesNothing(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/public_html/assets"))

	token := model.remote.requestToken
	updated, cmd := model.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

	if cmd != nil {
		t.Fatalf("enter on a file issued a listing")
	}
	if model.remote.requestToken != token {
		t.Fatalf("enter on a file started a request")
	}
}

func TestConnectFailureLeavesTheAppUsable(t *testing.T) {
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine(), err: errors.New("no route to host")}
	model := NewModel(localfs.New(), dialer, []session.Target{testTarget})
	model.width, model.height = 120, 36

	model = settle(t, model, model.Init())

	if model.state != connFailed {
		t.Fatalf("state = %v after a failed dial, want failed", model.state)
	}
	if model.connected() {
		t.Fatalf("a failed dial must not report a connection")
	}
	if !model.statusErr || !strings.Contains(model.status, "no route to host") {
		t.Fatalf("status = %q (err=%v), want the dial failure", model.status, model.statusErr)
	}
	// The local pane is unaffected by a remote failure.
	if len(model.local.entries) == 0 {
		t.Fatalf("local pane must still work after a failed connect")
	}
	if view := model.View(); !strings.Contains(view, "Not connected") {
		t.Fatalf("remote pane should say it is not connected\n%s", view)
	}
}

func TestTransfersRefuseToStartWhileDisconnected(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model = settle(t, model, model.disconnect())
	model = settle(t, model, func() tea.Msg { return disconnectedMsg{conn: model.conn} })

	model.focus = focusLocal
	model.local.entries = []domain.Entry{{Name: "a.txt", Size: 10}}
	model.local.cursor = 0

	model = press(t, model, runes("u"))

	if len(model.transfers) != 0 {
		t.Fatalf("queued %d transfers while disconnected, want none", len(model.transfers))
	}
	if !model.statusErr || !strings.Contains(model.status, "not connected") {
		t.Fatalf("status = %q, want a not-connected error", model.status)
	}
}

func TestDisconnectClearsTheRemoteSide(t *testing.T) {
	engine := newScriptedEngine()
	model, dialer := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: engine})
	model.focus = focusRemote
	if len(model.remote.entries) == 0 {
		t.Fatalf("expected the remote pane to be loaded before disconnecting")
	}

	conn := model.conn
	model = settle(t, model, model.disconnect())
	if !dialer.conns[0].closed {
		t.Fatalf("disconnect did not close the connection")
	}
	model = settle(t, model, func() tea.Msg { return disconnectedMsg{conn: conn} })

	if model.connected() || model.state != connDisconnected {
		t.Fatalf("state = %v after disconnect, want disconnected", model.state)
	}
	if model.conn != nil || model.remoteFS != nil || model.engine != nil {
		t.Fatalf("disconnect left a stale adapter wired in")
	}
	if len(model.remote.entries) != 0 || model.remote.path != "" {
		t.Fatalf("remote pane still shows %d entries at %q", len(model.remote.entries), model.remote.path)
	}
	if model.focus == focusRemote {
		t.Fatalf("focus stayed on a pane that no longer has contents")
	}
}

func TestDroppedConnectionFailsInFlightTransfers(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model.transfers = []domain.Transfer{
		{ID: 1, BytesTotal: 1000, Status: domain.Active},
		{ID: 2, BytesTotal: 1000, Status: domain.Queued},
		{ID: 3, BytesTotal: 1000, BytesDone: 1000, Status: domain.Done},
	}

	next, _ := model.Update(disconnectedMsg{conn: model.conn, err: errors.New("connection reset by peer")})
	model = next.(Model)

	if model.state != connFailed {
		t.Fatalf("state = %v after a drop, want failed", model.state)
	}
	for _, id := range []int{1, 2} {
		row := model.transfers[id-1]
		if row.Status != domain.Failed {
			t.Fatalf("transfer %d status = %v after the drop, want Failed", id, row.Status)
		}
		if row.Message != "connection reset by peer" {
			t.Fatalf("transfer %d message = %q, want the drop reason", id, row.Message)
		}
	}
	if model.transfers[2].Status != domain.Done {
		t.Fatalf("a finished transfer must not be re-marked by a drop")
	}
	if !strings.Contains(model.status, "connection lost") {
		t.Fatalf("status = %q, want it to report the lost connection", model.status)
	}
}

func TestLateConnectionIsClosedNotUsed(t *testing.T) {
	model, dialer := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	live := model.conn

	// A connection for a target the user has already moved away from.
	stale := &stubConn{fs: fakefs.NewRemote(), engine: newScriptedEngine(), done: make(chan error, 1)}
	other := session.Target{Name: "other", Protocol: "sftp", Host: "other.local", User: "allie"}

	next, cmd := model.Update(connectedMsg{target: other, conn: stale})
	model = next.(Model)
	if cmd != nil {
		cmd()
	}

	if model.conn != live {
		t.Fatalf("a stale connection replaced the live one")
	}
	if !stale.closed {
		t.Fatalf("a connection that arrived too late should be closed, not leaked")
	}
	_ = dialer
}

func TestConnectMenuOffersDisconnectWhenConnected(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})

	model = press(t, model, runes("c"))
	if model.overlay != overlayConnect {
		t.Fatalf("c did not open the connect overlay")
	}
	rows := model.connectRows()
	if len(rows) != 2 || !rows[0].disconnect {
		t.Fatalf("connect menu = %+v, want a disconnect action then the target", rows)
	}

	// Enter on the first row disconnects.
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.overlay != overlayNone {
		t.Fatalf("enter did not close the overlay")
	}
	model = settle(t, model, func() tea.Msg { return disconnectedMsg{conn: model.conn} })
	if model.connected() {
		t.Fatalf("choosing disconnect left the model connected")
	}

	// With nothing connected the menu is just the targets.
	model = press(t, model, runes("c"))
	rows = model.connectRows()
	if len(rows) != 1 || rows[0].disconnect {
		t.Fatalf("disconnected menu = %+v, want just the target", rows)
	}
}

func TestConnectMenuCursorStaysInRange(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model = press(t, model, runes("c"))

	for i := 0; i < 10; i++ {
		model = press(t, model, tea.KeyMsg{Type: tea.KeyDown})
	}
	if want := len(model.connectRows()) - 1; model.targetIndex != want {
		t.Fatalf("targetIndex = %d after paging down, want %d", model.targetIndex, want)
	}
	for i := 0; i < 10; i++ {
		model = press(t, model, tea.KeyMsg{Type: tea.KeyUp})
	}
	if model.targetIndex != 0 {
		t.Fatalf("targetIndex = %d after paging up, want 0", model.targetIndex)
	}
}

func TestReconnectingClosesThePreviousConnection(t *testing.T) {
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model, _ := loadedModelWithDialer(t, dialer)
	first := model.conn

	model = settle(t, model, model.connect(testTarget))

	if !dialer.conns[0].closed {
		t.Fatalf("reconnecting left the previous connection open")
	}
	if model.conn == first {
		t.Fatalf("reconnecting reused the old connection")
	}
	if !model.connected() {
		t.Fatalf("state = %v after reconnecting, want connected", model.state)
	}
}

func TestStartQueuedTransfersIsANoopWhileDisconnected(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model = settle(t, model, model.disconnect())
	model = settle(t, model, func() tea.Msg { return disconnectedMsg{conn: model.conn} })

	// Transfers left over from the connection that just ended must not be
	// handed to an engine that no longer exists.
	model.transfers = []domain.Transfer{{ID: 1, BytesTotal: 1000, Status: domain.Queued}}
	model.startQueuedTransfers()

	if got := countStatus(model.transfers, domain.Active); got != 0 {
		t.Fatalf("%d transfers went active while disconnected", got)
	}
	if model.transfers[0].Status != domain.Queued {
		t.Fatalf("transfer status = %v, want it left Queued", model.transfers[0].Status)
	}
}

func TestStaleDisconnectDoesNotTearDownTheLiveConnection(t *testing.T) {
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model, _ := loadedModelWithDialer(t, dialer)
	old := model.conn

	model = settle(t, model, model.connect(testTarget))
	live := model.conn
	if live == old {
		t.Fatalf("reconnect did not produce a new connection")
	}

	// The previous connection's watcher reports in after the new one is up.
	next, _ := model.Update(disconnectedMsg{conn: old, err: errors.New("old connection closed")})
	model = next.(Model)

	if model.conn != live {
		t.Fatalf("a stale disconnect tore down the live connection")
	}
	if !model.connected() {
		t.Fatalf("state = %v after a stale disconnect, want still connected", model.state)
	}
}

func TestQueuingSkipsDirectories(t *testing.T) {
	engine := newScriptedEngine()
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: engine})
	model.focus = focusLocal
	model.local.path = "/tmp"
	model.local.entries = []domain.Entry{
		{Name: "folder", Kind: domain.EntryDir},
		{Name: "file.txt", Kind: domain.EntryFile, Size: 1234},
	}
	model.local.selected = map[string]bool{"folder": true, "file.txt": true}

	model = press(t, model, runes("u"))

	if len(model.transfers) != 1 {
		t.Fatalf("queued %d transfers, want only the file", len(model.transfers))
	}
	if model.transfers[0].BytesTotal != 1234 {
		t.Fatalf("BytesTotal = %d, want the file's real size", model.transfers[0].BytesTotal)
	}
	if !strings.Contains(model.status, "skipped 1 folder") {
		t.Fatalf("status = %q, want it to mention the skipped folder", model.status)
	}
}

func TestQueuingOnlyDirectoriesReportsAnError(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model.focus = focusLocal
	model.local.entries = []domain.Entry{{Name: "folder", Kind: domain.EntryDir}}
	model.local.cursor = 0
	model.local.selected = map[string]bool{}

	model = press(t, model, runes("u"))

	if len(model.transfers) != 0 {
		t.Fatalf("queued %d transfers for a folder, want none", len(model.transfers))
	}
	if !model.statusErr || !strings.Contains(model.status, "recursive transfers are not supported") {
		t.Fatalf("status = %q (err=%v), want a clear explanation", model.status, model.statusErr)
	}
}

// TestFirstFileRowMatchesTheRenderedLayout pins the mouse row mapping to what
// the view actually draws.
//
// firstFileRow is a constant asserting how tall the chrome above the file
// panes is. Nothing enforced that, and it was wrong: the topbar laid itself
// out against the full width while its style added a column of padding on each
// side, so it wrapped onto a second row and shifted every row below it. The
// pane header and the column header wrapped at narrow widths too. Because the
// final view is clamped to the terminal height, a wrap is invisible — it just
// pushes the status bar off the bottom — so only a check like this catches it.
func TestFirstFileRowMatchesTheRenderedLayout(t *testing.T) {
	// The marker goes in the mode column, which renders near the left edge of
	// the row. A name would be truncated away entirely in a narrow pane and
	// the test would be measuring its own marker rather than the layout.
	const marker = "ZZfirstZZ"
	sizes := [][2]int{{60, 20}, {80, 24}, {100, 30}, {120, 36}, {160, 44}, {200, 50}}

	for _, size := range sizes {
		model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
		model.width, model.height = size[0], size[1]
		model.local.entries = []domain.Entry{{Name: "first", Mode: marker}, {Name: "second"}, {Name: "third"}}
		model.local.cursor, model.local.offset = 0, 0

		lines := strings.Split(model.View(), "\n")
		row := -1
		for index, line := range lines {
			if strings.Contains(ansi.Strip(line), marker) {
				row = index
				break
			}
		}
		if row != firstFileRow {
			t.Fatalf("%dx%d: first entry drawn on row %d, but firstFileRow is %d — clicks would be off by %d",
				size[0], size[1], row, firstFileRow, row-firstFileRow)
		}

		// A wrap anywhere in the chrome pushes the status bar off the bottom,
		// so its presence on the last row is a second check on the same thing.
		// The hint is truncated at narrow widths, so match its opening words.
		if last := ansi.Strip(lines[len(lines)-1]); !strings.Contains(last, "tab pane") {
			t.Fatalf("%dx%d: status bar missing from the last row, got %q", size[0], size[1], last)
		}
	}
}

func TestMouseClickRespectsTheScrollOffset(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model.width, model.height = 120, 36
	entries := make([]domain.Entry, 40)
	for i := range entries {
		entries[i] = domain.Entry{Name: string(rune('a' + i%26))}
	}
	model.local.entries = entries
	model.local.cursor, model.local.offset = 0, 7

	next, _ := model.updateMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: firstFileRow + 3})
	model = next.(Model)

	if want := 7 + 3; model.local.cursor != want {
		t.Fatalf("click three rows into a pane scrolled to %d selected %d, want %d", 7, model.local.cursor, want)
	}
}
