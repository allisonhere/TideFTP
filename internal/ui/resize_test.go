package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
	"tideftp/internal/session"
)

// TestViewSurvivesTinyTerminals renders every overlay (and none) across a
// sweep of very small terminal sizes, down to 1x1 and 0x0, and fails only
// on a panic — TestFirstFileRowMatchesTheRenderedLayout already pins exact
// layout at realistic sizes, so this is purely a "does it fall over"
// smoke test for sizes no realistic layout math was written against.
func TestViewSurvivesTinyTerminals(t *testing.T) {
	sizes := [][2]int{{0, 0}, {1, 1}, {2, 2}, {5, 3}, {10, 5}, {20, 8}, {1, 30}, {100, 1}}
	overlays := []overlayMode{overlayNone, overlayHelp, overlayConnect, overlayConflict, overlayTheme, overlayPreflight, overlaySettings, overlayHostKey}

	for _, size := range sizes {
		for _, overlay := range overlays {
			model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
			model.width, model.height = size[0], size[1]
			model.overlay = overlay
			if overlay == overlayTheme {
				model.themePicker.Open(model.theme.Name)
			}
			if overlay == overlayPreflight {
				model.preflight = &preflightScan{direction: domain.Download, files: make([]preflightFile, 3), folders: 1, totalBytes: 4096}
			}
			if overlay == overlayHostKey {
				model.hostKeyPrompt = &hostKeyPrompt{
					target: testTarget,
					err:    &session.UntrustedHostKeyError{Address: testTarget.Address(), Algorithm: "ssh-ed25519", Fingerprint: "SHA256:Example"},
				}
			}

			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("View panicked at %dx%d overlay=%v: %v", size[0], size[1], overlay, r)
					}
				}()
				model.View()
			}()
		}
	}
}

// TestViewShrinksAndGrowsWithoutPanicking drives a WindowSizeMsg sequence
// down to nothing and back up, the way a real terminal resize would.
func TestViewShrinksAndGrowsWithoutPanicking(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model.local.entries = []domain.Entry{{Name: "a"}, {Name: "b"}, {Name: "c"}}

	sizes := [][2]int{{120, 36}, {80, 24}, {40, 12}, {10, 4}, {1, 1}, {0, 0}, {60, 20}, {120, 36}}
	for _, size := range sizes {
		next, _ := model.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		model = next.(Model)
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("View panicked resizing to %dx%d: %v", size[0], size[1], r)
				}
			}()
			model.View()
		}()
	}
}
