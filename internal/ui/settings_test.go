package ui

import (
	"strings"
	"testing"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"tideftp/internal/config"
	"tideftp/internal/fakefs"
	"tideftp/internal/localfs"
	"tideftp/internal/session"
)

func TestSettingsOverlayOpensAndCloses(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())

	model = press(t, model, runes(","))
	if model.overlay != overlaySettings {
		t.Fatalf("overlay = %v, want overlaySettings", model.overlay)
	}
	if model.settingsCursor != 0 {
		t.Fatalf("settingsCursor = %d, want 0 on open", model.settingsCursor)
	}

	model = press(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.overlay != overlayNone {
		t.Fatalf("esc did not close the settings overlay, overlay = %v", model.overlay)
	}
}

func TestSettingsCursorWrapsBothWays(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model = press(t, model, runes(","))

	model = press(t, model, tea.KeyMsg{Type: tea.KeyUp})
	if want := int(settingsFieldCount) - 1; model.settingsCursor != want {
		t.Fatalf("cursor after up from row 0 = %d, want %d (wrapped to the last row)", model.settingsCursor, want)
	}

	model = press(t, model, tea.KeyMsg{Type: tea.KeyDown})
	if model.settingsCursor != 0 {
		t.Fatalf("cursor after down from the last row = %d, want 0 (wrapped)", model.settingsCursor)
	}
}

func TestSettingsTogglesShadowAndIconsAndPersists(t *testing.T) {
	var saved []config.Config
	save := func(c config.Config) error { saved = append(saved, c); return nil }
	cfg := config.Default()
	cfg.Shadow = true
	cfg.ShowIcons = true
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, []session.Target{testTarget}, cfg, save, nil)
	model.width, model.height = 120, 36

	model = press(t, model, runes(","))
	for model.settingsCursor != int(settingsFieldShadow) {
		model = press(t, model, tea.KeyMsg{Type: tea.KeyDown})
	}
	model = press(t, model, tea.KeyMsg{Type: tea.KeyLeft})
	if model.shadow {
		t.Fatalf("shadow = true after toggling, want false")
	}

	for model.settingsCursor != int(settingsFieldIcons) {
		model = press(t, model, tea.KeyMsg{Type: tea.KeyDown})
	}
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.showIcons {
		t.Fatalf("showIcons = true after toggling, want false")
	}

	if len(saved) < 2 {
		t.Fatalf("toggling settings rows should persist each change, saved = %d times", len(saved))
	}
	last := saved[len(saved)-1]
	if last.Shadow || last.ShowIcons {
		t.Fatalf("persisted config = %+v, want both Shadow and ShowIcons false", last)
	}
}

func TestSettingsCyclesDensity(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	if model.density != tideui.Compact {
		t.Fatalf("default density = %v, want Compact", model.density)
	}

	model = press(t, model, runes(","))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyDown}) // Theme -> Density
	model = press(t, model, tea.KeyMsg{Type: tea.KeyRight})

	if model.density != tideui.Comfortable {
		t.Fatalf("density after cycling = %v, want Comfortable", model.density)
	}
}

func TestSettingsMaxParallelRespectsDirectionAndClamps(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.maxParallel = 1
	model = press(t, model, runes(","))
	for model.settingsCursor != int(settingsFieldMaxParallel) {
		model = press(t, model, tea.KeyMsg{Type: tea.KeyDown})
	}

	model = press(t, model, tea.KeyMsg{Type: tea.KeyLeft})
	if model.maxParallel != 1 {
		t.Fatalf("maxParallel after - at the floor = %d, want clamped to 1", model.maxParallel)
	}

	model = press(t, model, tea.KeyMsg{Type: tea.KeyRight})
	if model.maxParallel != 2 {
		t.Fatalf("maxParallel after + = %d, want 2", model.maxParallel)
	}
}

func TestSettingsThemeCyclesLiveWithoutLeavingSettings(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	themes := appThemes()
	if len(themes) < 2 {
		t.Fatalf("need at least 2 themes to test cycling, got %d", len(themes))
	}
	model = press(t, model, runes(","))
	if model.settingsCursor != int(settingsFieldTheme) {
		t.Fatalf("expected to start on the Theme row")
	}
	start := model.theme.Name

	model = press(t, model, tea.KeyMsg{Type: tea.KeyRight})

	if model.overlay != overlaySettings {
		t.Fatalf("overlay after cycling the theme = %v, want to stay in overlaySettings", model.overlay)
	}
	if model.theme.Name == start {
		t.Fatalf("theme did not change after cycling right")
	}
	if model.theme.Name != themes[1].Name {
		t.Fatalf("theme after one right = %q, want %q (the next entry in appThemes)", model.theme.Name, themes[1].Name)
	}

	model = press(t, model, tea.KeyMsg{Type: tea.KeyLeft})
	if model.theme.Name != start {
		t.Fatalf("theme after cycling back left = %q, want the original %q", model.theme.Name, start)
	}

	// Cycling left from the first theme wraps to the last.
	model = press(t, model, tea.KeyMsg{Type: tea.KeyLeft})
	if model.theme.Name != themes[len(themes)-1].Name {
		t.Fatalf("theme after wrapping left = %q, want the last entry %q", model.theme.Name, themes[len(themes)-1].Name)
	}
}

func TestSettingsThemeRowOpensThePicker(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model = press(t, model, runes(","))
	if model.settingsCursor != int(settingsFieldTheme) {
		t.Fatalf("expected to start on the Theme row")
	}

	updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

	if model.overlay != overlayTheme {
		t.Fatalf("overlay after activating Theme = %v, want overlayTheme", model.overlay)
	}
}

func TestSettingsOverlayRendersEveryRow(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model = press(t, model, runes(","))

	plain := ansi.Strip(model.View())
	for _, want := range []string{"settings", "Theme", "Density", "Shadow", "Icons", "Max Parallel"} {
		if !strings.Contains(plain, want) {
			t.Errorf("settings overlay is missing %q", want)
		}
	}
}
