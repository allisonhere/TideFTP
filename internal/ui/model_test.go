package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
)

func TestShiftArrowsResizeLayout(t *testing.T) {
	model := NewModel(fakefs.NewRemote())
	startFile := model.fileSplit.Value()
	updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("shift+right")})
	model = updated.(Model)
	if model.fileSplit.Value() <= startFile {
		t.Fatalf("shift+right did not grow file split: got %v want > %v", model.fileSplit.Value(), startFile)
	}
	startBottom := model.bottomSplit.Value()
	updated, _ = model.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("shift+up")})
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

	updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("down")})
	model = updated.(Model)
	if model.bottomOffset != 1 {
		t.Fatalf("bottomOffset after one down = %d, want 1", model.bottomOffset)
	}

	for i := 0; i < 100; i++ {
		updated, _ = model.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("down")})
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
