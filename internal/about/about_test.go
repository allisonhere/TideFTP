package about

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func testOptions() Options {
	return Options{
		Version: "v9.9.9", Commit: "abc1234", BuildDate: "2026-10-08",
		Message: "Thanks for using TideFTP. Steady, resumable, and never in your way.",
		Author:  "Allie",
		Links:   []Link{{Label: "source", URL: "github.com/allisonhere/TideFTP"}},
	}
}

func plain(s string) string { return ansi.Strip(s) }

func TestRenderNeverOverflowsTheBox(t *testing.T) {
	sizes := [][2]int{{1, 1}, {5, 2}, {10, 3}, {20, 6}, {30, 10}, {40, 15}, {44, 20}, {60, 24}, {80, 24}, {80, 40}, {120, 30}, {200, 60}}
	for _, palette := range []int{0, 1, 2, 3} {
		for _, frame := range []int{0, 7, 20, 41, 63, 95, 140, 999} {
			for _, sz := range sizes {
				o := testOptions()
				o.Palette = palette
				out := New(o).WithFrame(frame).Render(sz[0], sz[1])
				lines := strings.Split(out, "\n")
				if len(lines) > sz[1] {
					t.Fatalf("palette %d frame %d %dx%d: %d lines, want <= %d", palette, frame, sz[0], sz[1], len(lines), sz[1])
				}
				for i, l := range lines {
					if w := lipgloss.Width(l); w > sz[0] {
						t.Fatalf("palette %d frame %d %dx%d: line %d is %d wide: %q", palette, frame, sz[0], sz[1], i, w, plain(l))
					}
				}
			}
		}
	}
}

func TestZeroSizeRendersNothing(t *testing.T) {
	if got := New(testOptions()).Render(0, 10); got != "" {
		t.Fatalf("got %q for width 0", got)
	}
	if got := New(testOptions()).Render(10, 0); got != "" {
		t.Fatalf("got %q for height 0", got)
	}
}

func TestFinishedScreenShowsEverything(t *testing.T) {
	out := plain(New(testOptions()).WithFrame(finalFrame).Render(80, 36))
	for _, want := range []string{
		"TideFTP v9.9.9", "commit abc1234", "built 2026-10-08",
		"Terminal Information Delivery Engine",
		"Thanks for using TideFTP", "— Allie",
		"github.com/allisonhere/TideFTP",
		"● ● ●", "> _",
		"space replay", "q close",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("finished screen is missing %q\n%s", want, out)
		}
	}
	// The block lettering spells TIDEFTP: the first row of T is a full bar.
	if !strings.Contains(out, "█████ ███ ████  █████ █████ █████ ████") {
		t.Errorf("wordmark top row missing\n%s", out)
	}
}

func TestIntroRevealsInOrder(t *testing.T) {
	tag := func(frame int) string {
		out := plain(New(testOptions()).WithFrame(frame).Render(80, 36))
		n := 0
		for i := len(tagline); i > 0; i-- {
			if strings.Contains(out, tagline[:i]) {
				n = i
				break
			}
		}
		return tagline[:n]
	}
	if got := tag(0); len(got) > 1 {
		t.Errorf("frame 0 already shows the tagline: %q", got)
	}
	prev := 0
	for _, f := range []int{34, 45, 55, 65, 75, 90, 140} {
		n := len(tag(f))
		if n < prev {
			t.Errorf("tagline shrank at frame %d: %d -> %d", f, prev, n)
		}
		prev = n
	}
	if tag(140) != tagline {
		t.Errorf("tagline never finished: %q", tag(140))
	}
	// The message is hidden at first and present once the intro is over.
	if strings.Contains(plain(New(testOptions()).WithFrame(10).Render(80, 36)), "Thanks for using") {
		t.Errorf("message visible at frame 10")
	}
	if !strings.Contains(plain(New(testOptions()).WithFrame(140).Render(80, 36)), "Thanks for using") {
		t.Errorf("message missing at the end")
	}
}

func TestWavesAnimate(t *testing.T) {
	a := New(testOptions()).WithFrame(150).Render(80, 30)
	b := New(testOptions()).WithFrame(151).Render(80, 30)
	if plain(a) == plain(b) {
		t.Fatalf("consecutive frames are identical: the waves are not moving")
	}
	// Reduced motion is a still image.
	o := testOptions()
	o.ReduceMotion = true
	m := New(o)
	if m.Init() != nil {
		t.Fatalf("reduced motion must not start a ticker")
	}
	before := m.Render(80, 30)
	m, cmd := m.Update(TickMsg{})
	if cmd != nil || m.Render(80, 30) != before {
		t.Fatalf("reduced motion changed on a tick")
	}
	if !strings.Contains(plain(before), "Thanks for using") {
		t.Fatalf("reduced motion should show the finished screen")
	}
}

func TestTickAdvancesAndReschedules(t *testing.T) {
	m := New(testOptions())
	if m.Init() == nil {
		t.Fatalf("Init should start the clock")
	}
	m2, cmd := m.Update(TickMsg{})
	if m2.frame != 1 || cmd == nil {
		t.Fatalf("tick: frame=%d cmd=%v", m2.frame, cmd)
	}
}

func TestKeys(t *testing.T) {
	m := New(testOptions()).WithFrame(100)
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyEsc}, {Type: tea.KeyEnter}, {Type: tea.KeyCtrlC},
		{Type: tea.KeyRunes, Runes: []rune("q")},
	} {
		_, cmd := m.Update(k)
		if cmd == nil {
			t.Fatalf("%v should close the screen", k)
		}
		if _, ok := cmd().(CloseMsg); !ok {
			t.Fatalf("%v produced %T, want CloseMsg", k, cmd())
		}
	}
	replayed, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if replayed.frame != 0 {
		t.Fatalf("space should replay the intro, frame=%d", replayed.frame)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if next.palette != 1 {
		t.Fatalf("t should cycle the palette, got %d", next.palette)
	}
	for i := 0; i < len(Palettes); i++ {
		next, _ = next.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	}
	if next.palette != 1 {
		t.Fatalf("palette cycling should wrap, got %d", next.palette)
	}
	// An unrelated key does nothing.
	same, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if cmd != nil || same.frame != 100 {
		t.Fatalf("x should be ignored")
	}
}

func TestWindowSizeIsRemembered(t *testing.T) {
	m, _ := New(testOptions()).WithFrame(finalFrame).Update(tea.WindowSizeMsg{Width: 70, Height: 28})
	lines := strings.Split(m.View(), "\n")
	if len(lines) > 28 {
		t.Fatalf("View used %d lines for a 28-line window", len(lines))
	}
}

func TestSmallTerminalsDropThingsInOrder(t *testing.T) {
	render := func(w, h int) string {
		return plain(New(testOptions()).WithFrame(finalFrame).Render(w, h))
	}
	// Short but wide: the big window logo goes first; the name, version, key
	// hint and waves stay.
	out := render(80, 16)
	if strings.Contains(out, "╭────────────────────────╮") {
		t.Errorf("the full window logo should not fit in 16 rows:\n%s", out)
	}
	for _, want := range []string{"TideFTP v9.9.9", "q close", "█"} {
		if !strings.Contains(out, want) {
			t.Errorf("80x16 lost %q:\n%s", want, out)
		}
	}
	// Shorter still: the mini logo and the message go before the version line.
	out = render(80, 11)
	if strings.Contains(out, "╭") {
		t.Errorf("no logo of any size should survive 11 rows:\n%s", out)
	}
	if strings.Contains(out, "Thanks for using") {
		t.Errorf("message should be dropped before the version line:\n%s", out)
	}
	if !strings.Contains(out, "TideFTP v9.9.9") {
		t.Errorf("version line dropped too early:\n%s", out)
	}
	// A narrow terminal still shows a key hint that fits.
	out = render(30, 12)
	if !strings.Contains(out, "q close") {
		t.Errorf("narrow terminal lost the close hint:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if lipgloss.Width(l) > 30 {
			t.Errorf("line wider than 30: %q", l)
		}
	}
}

func TestNoMessageOrLinksIsFine(t *testing.T) {
	out := plain(New(Options{Version: "dev"}).WithFrame(finalFrame).Render(80, 30))
	if !strings.Contains(out, "TideFTP dev") || strings.Contains(out, "┃") {
		t.Errorf("an empty message should leave no quote bar:\n%s", out)
	}
}

func TestWrap(t *testing.T) {
	for _, width := range []int{8, 20, 40} {
		for _, line := range wrap("a quick brown fox jumps over the lazy dog, supercalifragilisticexpialidocious!", width) {
			if n := len([]rune(line)); n > width {
				t.Errorf("width %d: line %q is %d wide", width, line, n)
			}
		}
	}
	if got := wrap("one\ntwo", 20); len(got) != 2 {
		t.Errorf("paragraphs should be kept: %q", got)
	}
	if got := wrap("   ", 10); len(got) != 0 {
		t.Errorf("blank text should wrap to nothing: %q", got)
	}
}

func TestBlendAndColourParsing(t *testing.T) {
	if got := blend("#000000", "#ffffff", 0.5); got != "#808080" {
		t.Errorf("blend midpoint = %s", got)
	}
	if blend("#123456", "#ffffff", 0) != "#123456" || blend("#123456", "#ffffff", 1) != "#ffffff" {
		t.Errorf("blend endpoints wrong")
	}
	if r, g, b := rgb("#fff"); r != 255 || g != 255 || b != 255 {
		t.Errorf("short hex: %d %d %d", r, g, b)
	}
	for _, bad := range []lipgloss.Color{"", "red", "#zzzzzz", "#12"} {
		if r, g, b := rgb(bad); r != 128 || g != 128 || b != 128 {
			t.Errorf("rgb(%q) should fall back to grey, got %d %d %d", bad, r, g, b)
		}
	}
}

func TestPaletteFromColorsAndHostThemes(t *testing.T) {
	p := PaletteFromColors("host", "#0d1f29", "#ffffff", "#888888", "#3b82f6", "#53d7e8", "#1f5263")
	if p.Bg != "#0d1f29" {
		t.Errorf("host background not carried: %q", p.Bg)
	}
	if p.Name != "host" || p.Waves[2] != "#53d7e8" || p.Waves[0] == p.Waves[1] {
		t.Errorf("derived palette looks wrong: %+v", p)
	}
	// A host palette that is garbage must still render, not panic.
	bad := PaletteFromColors("bad", "", "", "", "nonsense", "", "")
	o := testOptions()
	o.Custom = &bad
	if out := New(o).WithFrame(finalFrame).Render(80, 30); out == "" {
		t.Errorf("a bad palette rendered nothing")
	}
}

func TestPaletteIndexIsClamped(t *testing.T) {
	for _, i := range []int{-1, -7, 4, 99} {
		o := testOptions()
		o.Palette = i
		New(o).WithFrame(finalFrame).Render(80, 30) // must not panic
	}
}

func TestAnimationSettlesAndAnyKeyWakesIt(t *testing.T) {
	m := New(testOptions()).WithFrame(settleFrame - 1)
	m, cmd := m.Update(TickMsg{})
	if cmd != nil || !m.settled {
		t.Fatalf("at the settle frame the clock should stop: cmd=%v settled=%v", cmd, m.settled)
	}
	frozen := m.Render(80, 30)
	if _, cmd := m.Update(TickMsg{}); cmd != nil {
		t.Fatalf("a settled screen must not reschedule ticks")
	}
	// A key that does nothing else still wakes it, so a user who comes back to
	// a frozen screen sees it move again.
	woke, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if cmd == nil || woke.settled {
		t.Fatalf("a key should wake a settled screen: cmd=%v settled=%v", cmd, woke.settled)
	}
	if woke.Render(80, 30) == "" || frozen == "" {
		t.Fatalf("rendering failed")
	}
	// The close key still closes a settled screen.
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatalf("esc must still close")
	}
}

func TestReduceMotionNeverSettlesOrWakes(t *testing.T) {
	o := testOptions()
	o.ReduceMotion = true
	m := New(o)
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if cmd != nil || m2.frame != finalFrame {
		t.Fatalf("reduced motion changed on a key: cmd=%v frame=%d", cmd, m2.frame)
	}
}

func TestShortModalGetsTheMiniLogo(t *testing.T) {
	// Tall enough for the mini mark but not the full window.
	out := plain(New(testOptions()).WithFrame(finalFrame).Render(66, 22))
	if !strings.Contains(out, "╭──────────────╮") {
		t.Errorf("a 66x22 modal should show the mini window:\n%s", out)
	}
	if strings.Contains(out, "╭────────────────────────╮") {
		t.Errorf("the full window should not fit in 22 rows:\n%s", out)
	}
	if !strings.Contains(out, "Thanks for using") {
		t.Errorf("the mini logo must not crowd out the message:\n%s", out)
	}
	// With room, the full one is used instead.
	if full := plain(New(testOptions()).WithFrame(finalFrame).Render(66, 34)); !strings.Contains(full, "╭────────────────────────╮") {
		t.Errorf("a tall modal should show the full window:\n%s", full)
	}
}

func TestEveryLineIsExactlyTheWidth(t *testing.T) {
	// A modal panel is solid: every row must fill its width, or the padding
	// shows the panes underneath.
	for _, sz := range [][2]int{{66, 22}, {66, 34}, {80, 30}, {40, 14}} {
		out := New(testOptions()).WithFrame(finalFrame).Render(sz[0], sz[1])
		lines := strings.Split(out, "\n")
		if len(lines) != sz[1] {
			t.Errorf("%dx%d: %d rows, want exactly %d", sz[0], sz[1], len(lines), sz[1])
		}
		for i, l := range lines {
			if w := lipgloss.Width(l); w != sz[0] {
				t.Errorf("%dx%d: row %d is %d wide, want %d: %q", sz[0], sz[1], i, w, sz[0], plain(l))
			}
		}
	}
}

func TestBackgroundSurvivesStyleResets(t *testing.T) {
	// Colour output is off under `go test`, so check the painter directly.
	seq := "\x1b[48;2;13;31;41m"
	got := paintBackground("a\x1b[0mb\x1b[0mc", 6, seq)
	if !strings.HasPrefix(got, seq) {
		t.Errorf("line must start on the background: %q", got)
	}
	if n := strings.Count(got, seq); n != 3 {
		t.Errorf("background must be restored after each reset (want 3 switches), got %d: %q", n, got)
	}
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Errorf("line must end with a reset so the panel doesn't bleed: %q", got)
	}
	if lipgloss.Width(got) != 6 {
		t.Errorf("padded to %d, want 6", lipgloss.Width(got))
	}
	if got := paintBackground("ab", 5, ""); got != "ab   " {
		t.Errorf("with no background the line is only padded: %q", got)
	}
	if bgSequence("") != "" {
		t.Errorf("no background colour should give no sequence")
	}
}

func TestSignatureHeart(t *testing.T) {
	o := testOptions()
	o.Heart = true
	with := plain(New(o).WithFrame(finalFrame).Render(80, 36))
	if !strings.Contains(with, "— Allie ♥") {
		t.Errorf("signature should end with a heart:\n%s", with)
	}
	o.Heart = false
	if without := plain(New(o).WithFrame(finalFrame).Render(80, 36)); strings.Contains(without, "♥") || !strings.Contains(without, "— Allie") {
		t.Errorf("no heart when not asked:\n%s", without)
	}
}

func TestDefaultMessage(t *testing.T) {
	if DefaultAuthor != "Allie" || !strings.HasPrefix(DefaultMessage, "Thank you for using TideFTP") {
		t.Fatalf("defaults changed: %q / %q", DefaultAuthor, DefaultMessage)
	}
	// It must wrap cleanly at the modal width without losing words.
	var words int
	for _, l := range wrap(DefaultMessage, 54) {
		if len([]rune(l)) > 54 {
			t.Errorf("line too long: %q", l)
		}
		words += len(strings.Fields(l))
	}
	if words != len(strings.Fields(DefaultMessage)) {
		t.Errorf("wrapping dropped words")
	}
}

func TestCustomPaletteComesFirstAndCycles(t *testing.T) {
	custom := PaletteFromColors("host", "#101820", "#eeeeee", "#888888", "#ff8800", "", "#444444")
	o := testOptions()
	o.Custom = &custom
	m := New(o)
	if m.palettes[0].Name != "host" || len(m.palettes) != len(Palettes)+1 {
		t.Fatalf("custom palette should lead: %v", m.palettes[0].Name)
	}
	if custom.Accent2 == "" {
		t.Fatalf("a missing second accent should be derived from the first")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if m.palettes[m.palette].Name != Palettes[0].Name {
		t.Fatalf("t from the custom palette should reach the built-ins, got %s", m.palettes[m.palette].Name)
	}
	for i := 0; i < len(m.palettes); i++ {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	}
	if m.palettes[m.palette].Name != Palettes[0].Name {
		t.Fatalf("cycling should wrap around the whole list")
	}
	o.Palette = 0
	if got := New(o).palettes[New(o).palette].Name; got != "host" {
		t.Fatalf("starting palette 0 should be the custom one, got %s", got)
	}
}
