package ui

import (
	"strings"
	"testing"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"tideftp/internal/about"
)

// aboutModel is a golden-sized model with the build info pinned and motion
// reduced, so the screen is the finished, deterministic frame.
func aboutModel(t *testing.T, width, height int) Model {
	t.Helper()
	t.Setenv("TIDEFTP_REDUCE_MOTION", "1")
	old := aboutBuildInfo
	aboutBuildInfo = func() (string, string) { return "6690fc2", "2026-10-08" }
	t.Cleanup(func() { aboutBuildInfo = old })

	model := goldenModel(t)
	model.width, model.height = width, height
	model.version = "v0.4.0"
	return model
}

// openAboutFor opens the screen by its key, without settle(): the animation
// clock reschedules itself forever, which is right for a user and wrong for a
// helper that waits for commands to run out.
func openAboutFor(t *testing.T, model Model) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := model.updateKey(runes("A"))
	return updated.(Model), cmd
}

func TestAboutOpensWithShiftA(t *testing.T) {
	model, _ := openAboutFor(t, aboutModel(t, 100, 34))
	if model.overlay != overlayAbout {
		t.Fatalf("overlay = %v, want overlayAbout", model.overlay)
	}
	view := ansi.Strip(model.View())
	for _, want := range []string{"tideftp · about", "TideFTP v0.4.0", "commit 6690fc2", "built 2026-10-08",
		"Terminal Information Delivery Engine", "Thank you for using TideFTP", "— Allie ♥", "q close"} {
		if !strings.Contains(view, want) {
			t.Errorf("About screen is missing %q\n%s", want, view)
		}
	}
}

func TestAboutStartsItsClockUnlessMotionIsReduced(t *testing.T) {
	// Animated: opening returns the tick command.
	model := aboutModel(t, 100, 34)
	t.Setenv("TIDEFTP_REDUCE_MOTION", "")
	if _, cmd := openAboutFor(t, model); cmd == nil {
		t.Fatalf("opening the About screen should start its animation clock")
	}
	// Reduced motion: nothing ticks, so nothing redraws.
	t.Setenv("TIDEFTP_REDUCE_MOTION", "1")
	if _, cmd := openAboutFor(t, model); cmd != nil {
		t.Fatalf("TIDEFTP_REDUCE_MOTION should leave the screen still")
	}
}

func TestAboutTicksAnimateOnlyWhileOpen(t *testing.T) {
	model := aboutModel(t, 100, 34)
	t.Setenv("TIDEFTP_REDUCE_MOTION", "")
	model, _ = openAboutFor(t, model)

	before := ansi.Strip(model.View())
	for i := 0; i < 3; i++ {
		next, cmd := model.Update(about.TickMsg{})
		model = next.(Model)
		if cmd == nil {
			t.Fatalf("a tick while open must reschedule the next one")
		}
	}
	if after := ansi.Strip(model.View()); after == before {
		t.Fatalf("the screen did not change across ticks: the waves are not moving")
	}

	// Closed: a leftover tick is dropped and the loop ends.
	closed, _ := model.Update(about.CloseMsg{})
	model = closed.(Model)
	if model.overlay != overlayNone {
		t.Fatalf("CloseMsg should hide the overlay")
	}
	if _, cmd := model.Update(about.TickMsg{}); cmd != nil {
		t.Fatalf("a tick after closing must not keep the clock running")
	}
}

func TestAboutClosesWithQEscAndEnterButQDoesNotQuitTheApp(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyEnter}, runes("q")} {
		model, _ := openAboutFor(t, aboutModel(t, 100, 34))
		updated, cmd := model.updateKey(key)
		model = updated.(Model)
		if cmd == nil {
			t.Fatalf("%v should produce a close message", key)
		}
		msg := cmd()
		if _, ok := msg.(about.CloseMsg); !ok {
			t.Fatalf("%v produced %T, want about.CloseMsg (and in particular not tea.Quit)", key, msg)
		}
		next, _ := model.Update(msg)
		if next.(Model).overlay != overlayNone {
			t.Fatalf("%v did not close the overlay", key)
		}
	}
}

func TestAboutKeysDoNotLeakToThePanes(t *testing.T) {
	model, _ := openAboutFor(t, aboutModel(t, 100, 34))
	theme := model.theme.Name
	// `t` changes the About colours; it must not open the app's theme picker.
	updated, _ := model.updateKey(runes("t"))
	model = updated.(Model)
	if model.overlay != overlayAbout || model.theme.Name != theme {
		t.Fatalf("t leaked out of the About screen: overlay=%v theme=%s", model.overlay, model.theme.Name)
	}
	// Neither may a key meant for the panes (this one would open settings).
	updated, _ = model.updateKey(runes(","))
	if updated.(Model).overlay != overlayAbout {
		t.Fatalf(", leaked out of the About screen")
	}
}

func TestCtrlCStillQuitsFromAbout(t *testing.T) {
	model, _ := openAboutFor(t, aboutModel(t, 100, 34))
	updated, cmd := model.updateKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	after := updated.(Model)
	if after.overlay == overlayAbout {
		t.Fatalf("ctrl+c must work from inside every overlay, About included")
	}
	// The golden model has transfers running, so the app asks before quitting;
	// with none it would quit outright. Either way it left the About screen
	// and went down the normal quit path.
	if after.overlay != overlayQuitConfirm && cmd == nil {
		t.Fatalf("ctrl+c neither confirmed nor quit: overlay=%v", after.overlay)
	}
}

func TestAboutIsInTheCommandPaletteAndHelp(t *testing.T) {
	model := aboutModel(t, 100, 34)
	found := false
	for _, c := range model.paletteCommands() {
		if c.id == commandAbout && c.title == "About TideFTP" {
			found = true
		}
	}
	if !found {
		t.Fatalf("command palette has no About entry")
	}
	cmd := model.runPaletteCommand(commandAbout)
	_ = cmd
	if model.overlay != overlayAbout {
		t.Fatalf("the palette entry should open the About screen, overlay=%v", model.overlay)
	}
	helped := false
	for _, e := range helpEntries {
		if e.key == "A" && strings.Contains(e.label, "about") {
			helped = true
		}
	}
	if !helped {
		t.Fatalf("help does not document the A key")
	}
}

func TestAboutNeverOverflowsTheScreen(t *testing.T) {
	for _, sz := range [][2]int{{200, 60}, {120, 40}, {100, 34}, {100, 30}, {80, 24}, {60, 20}, {40, 14}, {30, 10}} {
		model, _ := openAboutFor(t, aboutModel(t, sz[0], sz[1]))
		lines := strings.Split(model.View(), "\n")
		if len(lines) > sz[1] {
			t.Errorf("%dx%d: View is %d rows tall", sz[0], sz[1], len(lines))
		}
		for i, l := range lines {
			if w := lipgloss.Width(l); w > sz[0] {
				t.Errorf("%dx%d: row %d is %d cells wide: %q", sz[0], sz[1], i, w, ansi.Strip(l))
			}
		}
	}
}

func TestAboutPaletteFollowsTheTheme(t *testing.T) {
	theme := tideui.Theme{
		Name: "t", Bg: "#101010", Fg: "#eeeeee", Dimmed: "#888888",
		Border: "#222222", BorderFocus: "#ff8800", Overlay: "#181818", OverlayBorder: "#333333",
	}
	p := aboutPalette(theme)
	if p.Bg != "#181818" || p.Accent != "#ff8800" || p.Fg != "#eeeeee" || p.Border != "#333333" {
		t.Fatalf("palette not taken from the theme: %+v", p)
	}
	// A theme with no overlay colour falls back to the plain background, and a
	// theme with no focus colour to its overlay border; neither may be blank.
	bare := aboutPalette(tideui.Theme{Name: "bare", Bg: "#0a0a0a", Fg: "#ffffff", Dimmed: "#999999", Border: "#444444", OverlayBorder: "#555555"})
	if bare.Bg != "#0a0a0a" || bare.Accent != "#555555" {
		t.Fatalf("fallbacks wrong: %+v", bare)
	}
}

func TestGoldenAboutOverlay(t *testing.T) {
	model, _ := openAboutFor(t, aboutModel(t, 100, 34))
	assertGolden(t, "about_overlay", ansi.Strip(model.View()))
}

func TestGoldenAboutOverlayShortTerminal(t *testing.T) {
	model, _ := openAboutFor(t, aboutModel(t, 100, 26))
	assertGolden(t, "about_overlay_short", ansi.Strip(model.View()))
}
