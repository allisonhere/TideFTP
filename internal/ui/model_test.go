package ui

import (
	"strings"
	"testing"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
)

func TestShiftArrowsResizeLayout(t *testing.T) {
	model := NewModel(fakefs.NewRemote())
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
	model := NewModel(fakefs.NewRemote())
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
	model := NewModel(fakefs.NewRemote())
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
	model := NewModel(fakefs.NewRemote())
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

func TestBottomPaneStaysPutWhenScrolledUpDuringTick(t *testing.T) {
	model := NewModel(fakefs.NewRemote())
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

	next, _ := model.Update(transferTick{})
	model = next.(Model)
	if model.bottomOffset != 1 {
		t.Fatalf("bottomOffset after a tick while scrolled up = %d, want unchanged at 1", model.bottomOffset)
	}
}

func TestManualScrollUpIsNotOverriddenByFollow(t *testing.T) {
	model := NewModel(fakefs.NewRemote())
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
	model := NewModel(fakefs.NewRemote())
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
	model := NewModel(fakefs.NewRemote())
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
	model := NewModel(fakefs.NewRemote())
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
	model := NewModel(fakefs.NewRemote())
	model.width, model.height = 120, 36
	// The row right after the top panes belongs to the transfers pane, using
	// the same height View draws with.
	next, _ := model.updateMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: 1 + model.topPaneHeight()})
	if got := next.(Model).focus; got != focusQueue {
		t.Fatalf("click below the top panes focused %v, want focusQueue", got)
	}
}

func TestEnteringDirectoryClearsSelection(t *testing.T) {
	model := NewModel(fakefs.NewRemote())
	model.focus = focusRemote
	model.remote.path = "/"
	model.refreshRemote()

	// Select every entry in the root, then walk into a subdirectory.
	updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyCtrlA})
	model = updated.(Model)
	if len(model.remote.selected) == 0 {
		t.Fatalf("expected ctrl+a to select the root entries")
	}
	for index, entry := range model.remote.entries {
		if entry.IsDir() {
			model.remote.cursor = index
			break
		}
	}
	updated, _ = model.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

	if len(model.remote.selected) != 0 {
		t.Fatalf("selection survived a directory change: %v", model.remote.selected)
	}
	if model.remote.cursor != 0 || model.remote.offset != 0 {
		t.Fatalf("entering a directory left cursor=%d offset=%d, want 0/0", model.remote.cursor, model.remote.offset)
	}

	// Going back up is a directory change too.
	model.remote.selected["public_html"] = true
	updated, _ = model.updateKey(tea.KeyMsg{Type: tea.KeyBackspace})
	model = updated.(Model)
	if len(model.remote.selected) != 0 {
		t.Fatalf("selection survived a move to the parent directory: %v", model.remote.selected)
	}
}

func TestFilePaneScrollsWithTheVisibleHeight(t *testing.T) {
	model := NewModel(fakefs.NewRemote())
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

func TestSimulatedTransfersCanFail(t *testing.T) {
	model := NewModel(fakefs.NewRemote())
	model.width, model.height = 100, 30
	model.transfers = []domain.Transfer{
		{ID: simulatedFailureEvery, BytesTotal: 100_000, Status: domain.Queued},
		{ID: simulatedFailureEvery + 1, BytesTotal: 100_000, Status: domain.Queued},
	}

	for i := 0; i < 20; i++ {
		next, _ := model.Update(transferTick{})
		model = next.(Model)
	}

	if got := model.transfers[0].Status; got != domain.Failed {
		t.Fatalf("transfer %d status = %v, want Failed", model.transfers[0].ID, got)
	}
	if model.transfers[0].Message == "" {
		t.Fatalf("a failed transfer should carry a message explaining why")
	}
	if got := model.transfers[1].Status; got != domain.Done {
		t.Fatalf("transfer %d status = %v, want Done", model.transfers[1].ID, got)
	}

	model.bottomTab = tabFailed
	if got := model.bottomRowCount(); got != 1 {
		t.Fatalf("failed tab row count = %d, want 1", got)
	}
	if view := model.View(); !strings.Contains(view, "connection reset by peer") {
		t.Fatalf("failed tab should render the transfer message\n%s", view)
	}
}

func TestParallelismIsCappedByMaxParallel(t *testing.T) {
	model := NewModel(fakefs.NewRemote())
	model.maxParallel = 3
	for i := 0; i < 10; i++ {
		model.transfers = append(model.transfers, domain.Transfer{ID: i + 1, BytesTotal: 10_000_000, Status: domain.Queued})
	}
	model.advanceTransfers()
	if got := countStatus(model.transfers, domain.Active); got != 3 {
		t.Fatalf("active transfers = %d, want maxParallel of 3", got)
	}
}
