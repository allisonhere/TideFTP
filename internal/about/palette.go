package about

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Palette is every colour the screen uses, so a host can theme it.
type Palette struct {
	Name string
	// Bg paints every cell, padding and gaps included, so the screen is a solid
	// panel in a modal instead of letting the panes underneath show through.
	// Empty leaves the terminal's own background.
	Bg     lipgloss.Color
	Fg     lipgloss.Color // main text
	Dim    lipgloss.Color // secondary text
	Accent lipgloss.Color // TIDE, links, quote bar
	// Accent2 colours "FTP" in the wordmark.
	Accent2 lipgloss.Color
	Spark   lipgloss.Color // the shimmer highlight
	Border  lipgloss.Color // the terminal-window outline
	// Waves, back to front.
	Waves [3]lipgloss.Color
}

// Palettes are the built-in looks; `t` cycles them. The first matches the
// app's default tide-night theme.
var Palettes = []Palette{
	{
		Name: "tide-night", Bg: "#0d1f29", Fg: "#d6edf3", Dim: "#678993", Accent: "#3b82f6", Accent2: "#53d7e8",
		Spark: "#ffffff", Border: "#1f5263", Waves: [3]lipgloss.Color{"#0b3a66", "#0a64b8", "#19a7ff"},
	},
	{
		Name: "aurora", Bg: "#0a1c17", Fg: "#e6f7ee", Dim: "#6f9a88", Accent: "#34d399", Accent2: "#a78bfa",
		Spark: "#ffffff", Border: "#24574a", Waves: [3]lipgloss.Color{"#124a3d", "#1d8a6c", "#5eead4"},
	},
	{
		Name: "sunset", Bg: "#1d1014", Fg: "#fdeee0", Dim: "#a1806a", Accent: "#fb7185", Accent2: "#fbbf24",
		Spark: "#fff7ed", Border: "#6b3a3a", Waves: [3]lipgloss.Color{"#5a1f3a", "#b4365a", "#fb923c"},
	},
	{
		Name: "mono", Bg: "#141414", Fg: "#e5e5e5", Dim: "#808080", Accent: "#bdbdbd", Accent2: "#ffffff",
		Spark: "#ffffff", Border: "#505050", Waves: [3]lipgloss.Color{"#2a2a2a", "#555555", "#8a8a8a"},
	},
}

// PaletteFromColors builds a palette from a host theme's colours (bg is the
// modal/overlay background), so the About
// screen follows whichever theme the user picked. The waves are derived from
// the accent: deep behind, bright in front.
func PaletteFromColors(name string, bg, fg, dim, accent, accent2, border lipgloss.Color) Palette {
	if accent2 == "" {
		// "FTP" in a lighter shade of the accent, so any theme works.
		accent2 = blend(accent, fg, 0.45)
	}
	return Palette{
		Name: name, Bg: bg, Fg: fg, Dim: dim, Accent: accent, Accent2: accent2,
		Spark: "#ffffff", Border: border,
		Waves: [3]lipgloss.Color{blend(accent, bg, 0.62), blend(accent, bg, 0.30), accent2},
	}
}

// rgb parses "#rrggbb"; anything else is mid-grey so a bad theme can't panic.
func rgb(c lipgloss.Color) (r, g, b int) {
	s := strings.TrimPrefix(string(c), "#")
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return 128, 128, 128
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 128, 128, 128
	}
	return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff)
}

// blend mixes a toward b by t (0 = a, 1 = b).
func blend(a, b lipgloss.Color, t float64) lipgloss.Color {
	if t <= 0 {
		return a
	}
	if t >= 1 {
		return b
	}
	ar, ag, ab := rgb(a)
	br, bg, bb := rgb(b)
	mix := func(x, y int) int { return int(float64(x) + (float64(y)-float64(x))*t + 0.5) }
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", mix(ar, br), mix(ag, bg), mix(ab, bb)))
}
