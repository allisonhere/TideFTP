package ui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"tideftp/internal/config"
	"tideftp/internal/fakefs"
	"tideftp/internal/localfs"
	"tideftp/internal/session"
)

// TestOverlayShadowRendersWhenEnabled guards the wiring into
// tideui.Renderer.OverlayModal: View must actually pass Model.shadow through
// to StyleOptions.ModalShadow and use the renderer's own contrast-aware
// shadow, not silently render identically regardless of the setting.
func TestOverlayShadowRendersWhenEnabled(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.Ascii)

	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, []session.Target{testTarget}, config.Default(), nil, nil)
	model.width, model.height = 120, 36
	model.overlay = overlayHelp

	model.shadow = true
	withShadow := model.View()

	model.shadow = false
	withoutShadow := model.View()

	if withShadow == withoutShadow {
		t.Fatalf("toggling Model.shadow did not change the rendered overlay")
	}
}
