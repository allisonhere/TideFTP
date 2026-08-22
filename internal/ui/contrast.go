package ui

import (
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Contrast handling for the colours this package paints itself.
//
// tideui runs every foreground in BuildStyles through its own readableText,
// but that helper is unexported and v0.2.2 is the latest release, so it cannot
// be called from here. Anywhere this package picks a colour of its own —
// directory names, transfer status, the progress bar — it was writing a raw
// theme colour onto whatever background the row happened to have, with no
// check at all. Selected and marked rows change that background, which is
// where text became unreadable.
//
// The maths below is tideui's, deliberately: same WCAG relative luminance,
// same thresholds. When tideui exports its helper, readableOn becomes a
// one-line call to it and the rest of this file goes away.
const (
	// textMinContrast is the WCAG AA floor for normal text, and the value
	// tideui uses for ordinary foregrounds.
	textMinContrast = 4.5
	// dimMinContrast is tideui's floor for deliberately muted text: low
	// enough to still read as secondary, high enough to be legible.
	dimMinContrast = 3.0
)

// readableOn returns preferred when it clears minimum against bg, and black or
// white otherwise. Losing a decorative colour is better than losing the text.
func readableOn(preferred, bg lipgloss.Color, minimum float64) lipgloss.Color {
	if preferred == "" {
		return contrastFg(bg)
	}
	// A colour that is not plain hex (an ANSI index, say) cannot be reasoned
	// about, so it is left alone rather than being replaced with a guess.
	if _, _, _, ok := hexToRGB(preferred); !ok {
		return preferred
	}
	if _, _, _, ok := hexToRGB(bg); !ok {
		return preferred
	}
	if contrastRatio(preferred, bg) >= minimum {
		return preferred
	}
	return contrastFg(bg)
}

func contrastFg(bg lipgloss.Color) lipgloss.Color {
	if relativeLuminance(bg) < 0.179 {
		return lipgloss.Color("#ffffff")
	}
	return lipgloss.Color("#000000")
}

func contrastRatio(a, b lipgloss.Color) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func relativeLuminance(c lipgloss.Color) float64 {
	r, g, b, ok := hexToRGB(c)
	if !ok {
		return 0
	}
	return 0.2126*srgbLinearize(r) + 0.7152*srgbLinearize(g) + 0.0722*srgbLinearize(b)
}

func srgbLinearize(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func hexToRGB(c lipgloss.Color) (r, g, b float64, ok bool) {
	value := strings.TrimPrefix(string(c), "#")
	if len(value) != 6 {
		return 0, 0, 0, false
	}
	ri, err1 := strconv.ParseUint(value[0:2], 16, 8)
	gi, err2 := strconv.ParseUint(value[2:4], 16, 8)
	bi, err3 := strconv.ParseUint(value[4:6], 16, 8)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, false
	}
	return float64(ri) / 255, float64(gi) / 255, float64(bi) / 255, true
}
