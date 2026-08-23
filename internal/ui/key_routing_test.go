package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
)

func TestSpaceTogglesSelectionOnTheCursorEntry(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusLocal
	model.local.entries = []domain.Entry{{Name: "a"}, {Name: "b"}}
	model.local.cursor = 0

	model = press(t, model, tea.KeyMsg{Type: tea.KeySpace})
	if !model.local.selected["a"] {
		t.Fatalf("space did not select the cursor entry: selected = %v", model.local.selected)
	}

	model = press(t, model, tea.KeyMsg{Type: tea.KeySpace})
	if model.local.selected["a"] {
		t.Fatalf("space again did not deselect the cursor entry: selected = %v", model.local.selected)
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
	model = press(t, model, runes("c"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyDown}) // Name (free text)
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
	model = press(t, model, runes("c"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyDown}) // Name
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

// TestOverlayOpenBlocksQuit confirms q closes an open overlay rather than
// falling through to the top-level quit binding: if it fell through, the
// overlay would still be open (only the overlay-close branch clears it).
func TestOverlayOpenBlocksQuit(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.overlay = overlayHelp

	model = press(t, model, runes("q"))

	if model.overlay != overlayNone {
		t.Fatalf("q with an overlay open left it at %v, want it closed rather than falling through to quit", model.overlay)
	}
}
