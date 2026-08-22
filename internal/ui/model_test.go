package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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
