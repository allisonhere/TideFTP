package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
)

// TestSpaceTogglesSelectionAndAdvances covers the ranger/nnn-style "toggle
// and move on" rhythm: space marks the cursor entry and steps to the next
// row, so a run of files can be selected with repeated space presses alone.
func TestSpaceTogglesSelectionAndAdvances(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusLocal
	model.local.entries = []domain.Entry{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	model.local.cursor = 0

	model = press(t, model, tea.KeyMsg{Type: tea.KeySpace})
	if !model.local.selected["a"] {
		t.Fatalf("space did not select the cursor entry: selected = %v", model.local.selected)
	}
	if model.local.cursor != 1 {
		t.Fatalf("cursor = %d after space, want it to advance to the next row", model.local.cursor)
	}

	model = press(t, model, tea.KeyMsg{Type: tea.KeySpace})
	if !model.local.selected["a"] || !model.local.selected["b"] {
		t.Fatalf("a second space did not select the new cursor entry too: selected = %v", model.local.selected)
	}
	if model.local.cursor != 2 {
		t.Fatalf("cursor = %d after a second space, want it to keep advancing", model.local.cursor)
	}

	// The cursor is now on the last row; space toggling it must not walk the
	// cursor past the end of the list.
	model = press(t, model, tea.KeyMsg{Type: tea.KeySpace})
	if !model.local.selected["c"] {
		t.Fatalf("space on the last row did not select it: selected = %v", model.local.selected)
	}
	if model.local.cursor != 2 {
		t.Fatalf("cursor = %d after space on the last row, want it clamped at 2", model.local.cursor)
	}

	// Moving back up and pressing space again deselects, the same toggle it
	// always was — advancing doesn't change what space does to one row.
	model = press(t, model, tea.KeyMsg{Type: tea.KeyUp})
	model = press(t, model, tea.KeyMsg{Type: tea.KeySpace})
	if model.local.selected["b"] {
		t.Fatalf("space on an already-selected row did not deselect it: selected = %v", model.local.selected)
	}
}

func TestEscClearsSelectionInTheFocusedPaneOnly(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusLocal
	model.local.selected = map[string]bool{"a": true}
	model.remote.selected = map[string]bool{"b": true}

	model = press(t, model, tea.KeyMsg{Type: tea.KeyEsc})

	if len(model.local.selected) != 0 {
		t.Fatalf("esc did not clear the focused (local) pane's selection: %v", model.local.selected)
	}
	if len(model.remote.selected) != 1 {
		t.Fatalf("esc touched the unfocused (remote) pane's selection: %v", model.remote.selected)
	}
	if !strings.Contains(model.status, "cleared") {
		t.Fatalf("status = %q, want it to mention the selection was cleared", model.status)
	}
}

// TestTransferRowFallsBackToStatusLabelWhenMessageIsEmpty exercises
// transferStatus, the label a row falls back to when nothing set Message.
func TestTransferRowFallsBackToStatusLabelWhenMessageIsEmpty(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model.width, model.height = 100, 30
	model.transfers = []domain.Transfer{{ID: 1, Status: domain.Failed, Source: "/a", Destination: "/b"}}
	model.bottomTab = tabFailed

	view := model.View()
	if !strings.Contains(view, "failed") {
		t.Fatalf("view does not show the transferStatus fallback label 'failed':\n%s", view)
	}
}

func TestConnectFormCursorMovesWithinAFreeTextField(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model = press(t, model, runes("c")) // opens on Name (free text)
	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlU})
	model = press(t, model, runes("hello"))
	if model.connectCursor != 5 {
		t.Fatalf("cursor after typing = %d, want 5 (end of \"hello\")", model.connectCursor)
	}

	model = press(t, model, tea.KeyMsg{Type: tea.KeyLeft})
	model = press(t, model, tea.KeyMsg{Type: tea.KeyLeft})
	if model.connectCursor != 3 {
		t.Fatalf("cursor after two lefts = %d, want 3", model.connectCursor)
	}

	model = press(t, model, tea.KeyMsg{Type: tea.KeyRight})
	if model.connectCursor != 4 {
		t.Fatalf("cursor after one right = %d, want 4", model.connectCursor)
	}
}

func TestConnectFormDeleteRemovesTheCharacterAfterTheCursor(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model = press(t, model, runes("c")) // opens on Name
	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlU})
	model = press(t, model, runes("hello"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyHome})

	model = press(t, model, tea.KeyMsg{Type: tea.KeyDelete})

	if model.connectForm.name != "ello" {
		t.Fatalf("name after delete at position 0 = %q, want %q", model.connectForm.name, "ello")
	}
	if model.connectCursor != 0 {
		t.Fatalf("cursor after delete = %d, want unchanged at 0", model.connectCursor)
	}
}

// TestTabCyclesThroughAllThreePanesBothWays pins the actual cycling order
// (Local -> Remote -> Queue -> Local) rather than each test just setting
// m.focus directly, the way most other tests in this package do.
func TestTabCyclesThroughAllThreePanesBothWays(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusLocal

	order := []focusPane{}
	for i := 0; i < 4; i++ {
		order = append(order, model.focus)
		model = press(t, model, tea.KeyMsg{Type: tea.KeyTab})
	}
	want := []focusPane{focusLocal, focusRemote, focusQueue, focusLocal}
	for i, got := range order {
		if got != want[i] {
			t.Fatalf("tab sequence[%d] = %v, want %v (full sequence %v)", i, got, want[i], order)
		}
	}

	model.focus = focusLocal
	model = press(t, model, tea.KeyMsg{Type: tea.KeyShiftTab})
	if model.focus != focusQueue {
		t.Fatalf("shift+tab from Local = %v, want Queue (reverse order)", model.focus)
	}
}

func TestLeftRightAndHLMoveBetweenTheTopPanes(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())

	cases := []struct {
		from focusPane
		key  tea.KeyMsg
		want focusPane
	}{
		{focusRemote, tea.KeyMsg{Type: tea.KeyLeft}, focusLocal},
		{focusLocal, tea.KeyMsg{Type: tea.KeyRight}, focusRemote},
		{focusRemote, runes("h"), focusLocal},
		{focusLocal, runes("l"), focusRemote},
		{focusQueue, runes("h"), focusLocal}, // also a quick way back up
		{focusQueue, tea.KeyMsg{Type: tea.KeyRight}, focusRemote},
		{focusLocal, runes("h"), focusLocal}, // already there: no-op
	}
	for _, c := range cases {
		model.focus = c.from
		model = press(t, model, c.key)
		if model.focus != c.want {
			t.Fatalf("%v + %v = %v, want %v", c.from, c.key, model.focus, c.want)
		}
	}
}

func TestBackspaceStillGoesToParentAfterHWasRebound(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/public_html/assets"))

	model = press(t, model, tea.KeyMsg{Type: tea.KeyBackspace})
	if model.remote.path != "/public_html" {
		t.Fatalf("backspace left the pane at %q, want /public_html", model.remote.path)
	}
}

// TestSixOpensTheStatsTab confirms the number key alongside 1-5 reaches the
// Stats tab and its cursor/scroll are inert, the same treatment tabLog gets.
func TestSixOpensTheStatsTab(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusQueue
	model.bottomTab = tabQueue

	model = press(t, model, runes("6"))

	if model.bottomTab != tabStats {
		t.Fatalf("bottomTab = %v after 6, want tabStats", model.bottomTab)
	}
	before := model.bottomCursor
	model = press(t, model, tea.KeyMsg{Type: tea.KeyDown})
	if model.bottomCursor != before {
		t.Fatalf("down on the Stats tab moved bottomCursor to %d, want it to stay inert like tabLog", model.bottomCursor)
	}
}

// TestOverlayOpenBlocksQuit confirms q on a confirmation-style overlay closes
// it rather than falling through to the top-level quit binding.
func TestOverlayOpenBlocksQuit(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.overlay = overlayPreflight

	model = press(t, model, runes("q"))

	if model.overlay != overlayNone {
		t.Fatalf("q with an overlay open left it at %v, want it closed rather than falling through to quit", model.overlay)
	}
}

// TestHelpOverlaySwallowsQuitKey confirms q in the help search field is typed
// into the query (so terms like "quit" are searchable) and never falls through
// to the top-level quit binding.
func TestHelpOverlaySwallowsQuitKey(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model = press(t, model, runes("?"))

	model = press(t, model, runes("q"))

	if model.overlay != overlayHelp {
		t.Fatalf("q in help left overlay=%v, want it still open", model.overlay)
	}
	if model.helpQuery != "q" {
		t.Fatalf("helpQuery = %q, want \"q\"", model.helpQuery)
	}
}

func TestCtrlKOpensCommandPalette(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())

	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlK})

	if model.overlay != overlayCommandPalette {
		t.Fatalf("overlay = %v after ctrl+k, want command palette", model.overlay)
	}
	plain := strings.ToLower(model.View())
	if !strings.Contains(plain, "command palette") || !strings.Contains(plain, "refresh") {
		t.Fatalf("command palette view missing expected content:\n%s", plain)
	}
}

func TestCommandPaletteFiltersCommands(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlK})

	model = press(t, model, runes("theme"))

	commands := model.filteredPaletteCommands()
	if len(commands) != 1 || commands[0].id != commandTheme {
		t.Fatalf("filtered commands = %+v, want only Theme picker", commands)
	}
}

func TestCommandPaletteRunsSelectedCommand(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlK})
	model = press(t, model, runes("settings"))

	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if model.overlay != overlaySettings {
		t.Fatalf("overlay after running Settings = %v, want settings", model.overlay)
	}
}

func TestCommandPaletteBrowseIdentityOpensPicker(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlK})
	model = press(t, model, runes("identity"))

	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if model.overlay != overlayConnect || !model.connectIdentityBrowse || model.connectField != connectFieldIdentity {
		t.Fatalf("identity command left overlay=%v browse=%v field=%v, want connect identity browser", model.overlay, model.connectIdentityBrowse, model.connectField)
	}
}

func TestHelpSearchFiltersRows(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model = press(t, model, runes("?"))

	model = press(t, model, runes("rename"))

	if model.overlay != overlayHelp {
		t.Fatalf("overlay = %v, want help", model.overlay)
	}
	entries := model.filteredHelpEntries()
	if len(entries) != 1 || entries[0].key != "f2" || entries[0].label != "rename item" {
		t.Fatalf("filtered help entries = %+v, want only rename item", entries)
	}
	if !strings.Contains(strings.ToLower(model.View()), "rename item") {
		t.Fatalf("help search should render rename row")
	}
}

func TestHelpSearchAcceptsReservedLetters(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model = press(t, model, runes("?"))

	for _, r := range "queue" { // q, u, e — none may be swallowed as commands
		model = press(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	if model.overlay != overlayHelp {
		t.Fatalf("overlay = %v, want help still open", model.overlay)
	}
	if model.helpQuery != "queue" {
		t.Fatalf("helpQuery = %q, want \"queue\"", model.helpQuery)
	}
	entries := model.filteredHelpEntries()
	if len(entries) != 1 || entries[0].key != "x" {
		t.Fatalf("filtered entries = %+v, want only the queue-pane row", entries)
	}
}

func TestHelpOverlayScrolls(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.height = 18
	model = press(t, model, runes("?"))

	model = press(t, model, tea.KeyMsg{Type: tea.KeyPgDown})

	if model.helpOffset == 0 {
		t.Fatalf("help offset did not move after pgdown")
	}
	plain := strings.ToLower(model.View())
	if !strings.Contains(plain, "rows") {
		t.Fatalf("help view should show scroll position:\n%s", plain)
	}
}
