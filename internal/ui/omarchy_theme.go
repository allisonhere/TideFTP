package ui

import (
	"fmt"
	"math"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/allisonhere/tideui"

	"tideftp/internal/omarchy"
)

// themeNameMatchOmarchy is the pseudo-theme that follows the current Omarchy
// desktop theme. Its colours are resolved at runtime (see resolveOmarchyTheme),
// so it is never a static member of appThemes' fixed list — omarchyPickerEntry
// supplies a freshly resolved entry each time the list is built.
const themeNameMatchOmarchy = "match-omarchy"

// omarchyThemeFromPalette maps a raw Omarchy palette onto a tideui.Theme.
// tideui.BuildStyles already runs every foreground through its own contrast
// correction, so this only has to keep the handful of raw background-ish
// fields BuildStyles trusts (Bg, StatusBar, BorderFocus/Border as header
// backgrounds) sane.
func omarchyThemeFromPalette(p omarchy.Palette) tideui.Theme {
	bg := lipgloss.Color(p.Background)
	fg := lipgloss.Color(p.Foreground)

	accent := firstColor(p.Accent, p.Foreground)
	muted := firstColor(p.Muted, string(mixHex(fg, bg, 0.5)))

	status := lipgloss.Color(p.StatusBg)
	if status == "" || contrastRatio(status, bg) < 1.12 {
		// A status bar that matches the page background vanishes; nudge it.
		status = mixHex(bg, fg, 0.14)
	}

	border := muted
	if border == "" || border == bg {
		border = mixHex(bg, fg, 0.35)
	}
	if accent == "" || accent == bg {
		accent = readableOn(fg, bg, textMinContrast)
	}

	return tideui.Theme{
		Name:          themeNameMatchOmarchy,
		Bg:            bg,
		Fg:            readableOn(fg, bg, textMinContrast),
		Border:        border,
		BorderFocus:   accent,
		Selected:      firstColor(p.Selection, string(accent)),
		Unread:        firstColor(p.Ok, string(accent)),
		Dimmed:        muted,
		StatusBar:     status,
		StatusFg:      readableOn(fg, status, textMinContrast),
		Error:         firstColor(p.Error, string(accent)),
		Overlay:       status,
		OverlayBorder: accent,
	}
}

func firstColor(preferred, fallback string) lipgloss.Color {
	if preferred != "" {
		return lipgloss.Color(preferred)
	}
	return lipgloss.Color(fallback)
}

// mixHex blends a toward b by amt (0..1) in sRGB space.
func mixHex(a, b lipgloss.Color, amt float64) lipgloss.Color {
	ar, ag, ab, ok1 := hexToRGB(a)
	br, bg, bb, ok2 := hexToRGB(b)
	if !ok1 || !ok2 {
		return a
	}
	amt = math.Max(0, math.Min(1, amt))
	c := func(x, y float64) int { return int(math.Round((x*(1-amt) + y*amt) * 255)) }
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", c(ar, br), c(ag, bg), c(ab, bb)))
}

// ── resolution + short-lived cache ───────────────────────────────────────────

var omarchyCache struct {
	mu    sync.Mutex
	theme tideui.Theme
	sig   string
	ok    bool
	fresh time.Time
}

// omarchyCacheTTL bounds how often the Omarchy palette is re-read while the
// theme list is being rebuilt (once per settings keystroke, say).
const omarchyCacheTTL = 1500 * time.Millisecond

// resolveOmarchyTheme returns the contrast-corrected current Omarchy theme,
// using a short-lived cache so repeated appThemes() calls do not each shell out
// to omarchy-theme-color. ok is false when Omarchy is not available.
func resolveOmarchyTheme() (tideui.Theme, bool) {
	omarchyCache.mu.Lock()
	defer omarchyCache.mu.Unlock()

	if time.Since(omarchyCache.fresh) < omarchyCacheTTL {
		return omarchyCache.theme, omarchyCache.ok
	}
	sig := omarchy.CurrentSignature()
	if sig != "" && sig == omarchyCache.sig && omarchyCache.ok {
		omarchyCache.fresh = time.Now()
		return omarchyCache.theme, true
	}

	p, ok := omarchy.CurrentPalette()
	omarchyCache.fresh = time.Now()
	omarchyCache.sig = sig
	omarchyCache.ok = ok
	if !ok {
		omarchyCache.theme = tideui.Theme{}
		return tideui.Theme{}, false
	}
	omarchyCache.theme = omarchyThemeFromPalette(p)
	return omarchyCache.theme, true
}

// omarchySignatureNow is the cheap change-detection token for the active
// Omarchy theme (empty when Omarchy is unavailable).
func omarchySignatureNow() string { return omarchy.CurrentSignature() }

// forceOmarchyRefresh drops the cache so the next resolveOmarchyTheme re-reads.
func forceOmarchyRefresh() {
	omarchyCache.mu.Lock()
	omarchyCache.fresh = time.Time{}
	omarchyCache.mu.Unlock()
}

// omarchyPickerEntry is the "match-omarchy" row shown in the theme picker and
// settings cycle: the live-resolved theme, or a labelled placeholder (so the
// row is always selectable) when Omarchy is not installed.
func omarchyPickerEntry() tideui.Theme {
	if t, ok := resolveOmarchyTheme(); ok {
		return t
	}
	placeholder := tideNight
	placeholder.Name = themeNameMatchOmarchy
	return placeholder
}

// ── live-follow poll ─────────────────────────────────────────────────────────

type omarchyTickMsg struct{}

const omarchyTickInterval = 2 * time.Second

func omarchyTickCmd() tea.Cmd {
	return tea.Tick(omarchyTickInterval, func(time.Time) tea.Msg { return omarchyTickMsg{} })
}

// applyOmarchyTick re-resolves the palette when the desktop theme changed and
// re-arms itself while "match-omarchy" is active; it stops (returns nil) once
// the theme is something else.
func (m *Model) applyOmarchyTick() tea.Cmd {
	if m.theme.Name != themeNameMatchOmarchy {
		m.omarchyWatching = false
		return nil
	}
	m.omarchyWatching = true
	if sig := omarchy.CurrentSignature(); sig != m.omarchySig {
		m.omarchySig = sig
		forceOmarchyRefresh()
		if t, ok := resolveOmarchyTheme(); ok {
			m.theme = t
		}
	}
	return omarchyTickCmd()
}

// startOmarchyWatch records the current signature and returns the poll command
// when "match-omarchy" is active and no poll is already running.
func (m *Model) startOmarchyWatch() tea.Cmd {
	if m.theme.Name != themeNameMatchOmarchy {
		return nil
	}
	m.omarchySig = omarchy.CurrentSignature()
	if m.omarchyWatching {
		return nil
	}
	m.omarchyWatching = true
	return omarchyTickCmd()
}
