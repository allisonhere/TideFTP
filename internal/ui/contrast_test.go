package ui

import (
	"testing"

	"github.com/allisonhere/tideui"
	"github.com/charmbracelet/lipgloss"

	"tideftp/internal/domain"
)

// allThemes is every theme the picker offers, so a colour that only works on
// tide-night cannot slip through.
func allThemes() []tideui.Theme { return appThemes() }

func checkContrast(t *testing.T, label string, fg, bg lipgloss.Color, floor float64) {
	t.Helper()
	if _, _, _, ok := hexToRGB(fg); !ok {
		return
	}
	if _, _, _, ok := hexToRGB(bg); !ok {
		return
	}
	if ratio := contrastRatio(fg, bg); ratio < floor {
		t.Errorf("%s: contrast %.2f between fg %s and bg %s, want at least %.1f", label, ratio, fg, bg, floor)
	}
}

// TestEntryRowColoursAreReadable covers every theme against every row state.
// The colours this package picks for itself — directory names, symlink names,
// the file metadata — were written straight onto the row background with no
// check, and selection and marking both change that background.
func TestEntryRowColoursAreReadable(t *testing.T) {
	kinds := map[string]domain.EntryKind{
		"file":    domain.EntryFile,
		"dir":     domain.EntryDir,
		"symlink": domain.EntrySymlink,
	}
	states := []struct {
		name           string
		cursor, marked bool
		hidden         bool
	}{
		{name: "plain"},
		{name: "cursor", cursor: true},
		{name: "marked", marked: true},
		{name: "hidden", hidden: true},
		{name: "hidden+cursor", hidden: true, cursor: true},
		{name: "hidden+marked", hidden: true, marked: true},
	}

	for _, theme := range allThemes() {
		renderer := tideui.NewRenderer(theme, tideui.StyleOptions{Density: tideui.Compact})
		for kindName, kind := range kinds {
			for _, state := range states {
				entry := domain.Entry{Name: "sample", Kind: kind, Hidden: state.hidden}
				bg, fg, name := entryPalette(renderer, entry, state.cursor, state.marked)

				floor := textMinContrast
				if state.hidden && !state.cursor && !state.marked {
					floor = dimMinContrast
				}
				label := theme.Name + "/" + kindName + "/" + state.name
				checkContrast(t, label+" meta", fg, bg, floor)
				checkContrast(t, label+" name", name, bg, floor)
			}
		}
	}
}

// TestTransferRowColoursAreReadable does the same for the transfers pane,
// where status colour carries meaning and so must stay legible.
func TestTransferRowColoursAreReadable(t *testing.T) {
	statuses := map[string]domain.TransferStatus{
		"queued":   domain.Queued,
		"active":   domain.Active,
		"failed":   domain.Failed,
		"canceled": domain.Canceled,
		"done":     domain.Done,
	}

	for _, theme := range allThemes() {
		renderer := tideui.NewRenderer(theme, tideui.StyleOptions{Density: tideui.Compact})
		for name, status := range statuses {
			for _, cursor := range []bool{false, true} {
				bg, fg, accent := transferPalette(renderer, status, cursor)

				floor := textMinContrast
				if (status == domain.Done || status == domain.Queued) && !cursor {
					floor = dimMinContrast
				}
				label := theme.Name + "/" + name
				if cursor {
					label += "/cursor"
				}
				checkContrast(t, label+" text", fg, bg, floor)
				checkContrast(t, label+" accent", accent, bg, floor)
			}
		}
	}
}

// TestReadableOnFallsBackWhenContrastFails pins the helper's own behaviour.
func TestReadableOnFallsBackWhenContrastFails(t *testing.T) {
	dark := lipgloss.Color("#07151d")
	light := lipgloss.Color("#f5f5f5")

	// A colour that already reads well is left alone.
	if got := readableOn(lipgloss.Color("#d6edf3"), dark, textMinContrast); got != lipgloss.Color("#d6edf3") {
		t.Fatalf("a colour that passes the check was replaced with %s", got)
	}
	// One that does not gives way to whichever of black or white reads.
	if got := readableOn(lipgloss.Color("#0a1a22"), dark, textMinContrast); got != lipgloss.Color("#ffffff") {
		t.Fatalf("dark-on-dark resolved to %s, want white", got)
	}
	if got := readableOn(lipgloss.Color("#fafafa"), light, textMinContrast); got != lipgloss.Color("#000000") {
		t.Fatalf("light-on-light resolved to %s, want black", got)
	}
	// A colour that cannot be parsed is left alone rather than guessed at.
	ansiColor := lipgloss.Color("5")
	if got := readableOn(ansiColor, dark, textMinContrast); got != ansiColor {
		t.Fatalf("a non-hex colour was rewritten to %s", got)
	}
}

// TestContrastRatioMatchesKnownValues checks the maths against the WCAG
// extremes, since every threshold above depends on it being right.
func TestContrastRatioMatchesKnownValues(t *testing.T) {
	white, black := lipgloss.Color("#ffffff"), lipgloss.Color("#000000")
	if got := contrastRatio(white, black); got < 20.9 || got > 21.1 {
		t.Fatalf("white on black = %.2f, want 21", got)
	}
	if got := contrastRatio(white, white); got < 0.99 || got > 1.01 {
		t.Fatalf("white on white = %.2f, want 1", got)
	}
}
