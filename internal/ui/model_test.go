package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
	"tideftp/internal/localfs"
	"tideftp/internal/transfer"
	"tideftp/internal/vfs"
)

// loadedModel builds a model over the fake adapters and settles its initial
// listings, so a test can treat both panes as already loaded. It goes through
// refresh rather than Init because Init also starts the transfer event pump,
// which blocks on an open channel.
func loadedModel(t *testing.T, engine transfer.Engine) Model {
	t.Helper()
	return loadedModelOver(t, localfs.New(), fakefs.NewRemote(), engine)
}

func loadedModelOver(t *testing.T, local, remote vfs.FS, engine transfer.Engine) Model {
	t.Helper()
	model := NewModel(local, remote, engine)
	model.width, model.height = 120, 36
	return settle(t, model, model.refresh())
}

// settle runs cmd and everything it produces against the model until nothing
// is left, so tests can treat asynchronous listings as if they were
// synchronous. It must not be handed a command that blocks (waitForTransferEvent
// on an open channel), which is why tests drive listings directly.
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
		switch msg := next().(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, msg...)
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

func TestQuitSchedulesAnEngineShutdown(t *testing.T) {
	engine := newScriptedEngine()
	model := loadedModel(t, engine)

	_, cmd := model.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatalf("quit produced no command, so nothing shuts the engine down")
	}
	// tea.Sequence hands its commands to the runtime rather than running them,
	// so the sequence itself cannot be executed here. Check the shutdown
	// command it carries does close the engine.
	if msg := closeEngine(engine)(); msg != nil {
		t.Fatalf("closeEngine returned %v, want no message", msg)
	}
	if !engine.closed {
		t.Fatalf("closeEngine did not close the engine")
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

func TestInitLoadsBothPanes(t *testing.T) {
	engine := newScriptedEngine()
	// Closing the stream lets Init's event pump return instead of blocking.
	close(engine.events)

	model := NewModel(localfs.New(), fakefs.NewRemote(), engine)
	model.width, model.height = 120, 36
	if !model.local.loading || !model.remote.loading {
		t.Fatalf("both panes should start in their loading state")
	}
	if len(model.remote.entries) != 0 {
		t.Fatalf("a constructor cannot list asynchronously; entries should be empty")
	}

	model = settle(t, model, model.Init())

	if model.local.loading || model.remote.loading {
		t.Fatalf("panes still loading after Init settled")
	}
	if len(model.remote.entries) == 0 {
		t.Fatalf("remote pane has no entries after Init")
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
