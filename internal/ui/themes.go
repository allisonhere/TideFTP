package ui

import (
	"github.com/allisonhere/tideui"
	"github.com/charmbracelet/lipgloss"
)

var tideNight = tideui.Theme{
	Name:          "tide-night",
	Bg:            lipgloss.Color("#07151d"),
	Fg:            lipgloss.Color("#d6edf3"),
	Border:        lipgloss.Color("#1f5263"),
	BorderFocus:   lipgloss.Color("#53d7e8"),
	Selected:      lipgloss.Color("#164757"),
	Unread:        lipgloss.Color("#8bdc9b"),
	Dimmed:        lipgloss.Color("#678993"),
	StatusBar:     lipgloss.Color("#0b2632"),
	StatusFg:      lipgloss.Color("#d6edf3"),
	Error:         lipgloss.Color("#ff7f8a"),
	Overlay:       lipgloss.Color("#0d1f29"),
	OverlayBorder: lipgloss.Color("#53d7e8"),
}

func appThemes() []tideui.Theme {
	themes := []tideui.Theme{tideNight}
	themes = append(themes, tideui.BuiltinThemes...)
	return themes
}
