package ui

import (
	"os"
	"runtime/debug"
	"strings"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/about"
)

// aboutPanelWidth is the About modal's inner width, matching the other soft
// modals (64–70 columns).
const aboutPanelWidth = 70

// aboutBuild reads the commit and date Go stamps into a binary built inside a
// git checkout. Both are empty for `go run`, a release tarball built without
// VCS info, and tests; the screen simply leaves them out.
func aboutBuild() (commit, date string) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", ""
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			commit = setting.Value
			if len(commit) > 7 {
				commit = commit[:7]
			}
		case "vcs.time":
			if len(setting.Value) >= 10 {
				date = setting.Value[:10]
			}
		}
	}
	return commit, date
}

// aboutPalette gives the About screen the active theme's colours, so it looks
// like part of the app rather than a pasted-in splash.
func aboutPalette(t tideui.Theme) about.Palette {
	bg := t.Overlay
	if bg == "" {
		bg = t.Bg
	}
	accent := t.BorderFocus
	if accent == "" {
		accent = t.OverlayBorder
	}
	border := t.OverlayBorder
	if border == "" {
		border = t.Border
	}
	return about.PaletteFromColors(t.Name, bg, t.Fg, t.Dimmed, accent, "", border)
}

// aboutBuildInfo is aboutBuild, replaceable so a golden test is not at the mercy
// of whichever commit the test binary happens to be built from.
var aboutBuildInfo = aboutBuild

func (m Model) aboutOptions() about.Options {
	commit, date := aboutBuildInfo()
	palette := aboutPalette(m.theme)
	return about.Options{
		Version:   m.version,
		Commit:    commit,
		BuildDate: date,
		Message:   about.DefaultMessage,
		Author:    about.DefaultAuthor,
		Heart:     true,
		Links:     []about.Link{{Label: "source", URL: "github.com/allisonhere/TideFTP"}},
		Custom:    &palette,
		// For anyone who finds animation distracting, or is on a slow link.
		ReduceMotion: os.Getenv("TIDEFTP_REDUCE_MOTION") != "",
	}
}

// openAbout shows the About modal and starts its animation clock.
func (m *Model) openAbout() tea.Cmd {
	m.about = about.New(m.aboutOptions())
	m.overlay = overlayAbout
	m.setStatus("about TideFTP")
	return m.about.Init()
}

// handleAboutKey forwards a key to the screen. Closing comes back as
// about.CloseMsg, which Update turns into hiding the overlay.
func (m *Model) handleAboutKey(msg tea.KeyMsg) tea.Cmd {
	var cmd tea.Cmd
	m.about, cmd = m.about.Update(msg)
	return cmd
}

// aboutSize is the modal's content box: as wide as the other soft modals when
// the terminal allows, and as tall as fits with its frame and a margin.
func (m Model) aboutSize() (width, height int) {
	width = min(aboutPanelWidth, max(24, m.width-6))
	height = min(28, max(8, m.height-8))
	return width, height
}

func (m Model) renderAboutOverlay(renderer tideui.Renderer) *tideui.Overlay {
	width, height := m.aboutSize()
	overlay := renderer.SoftPanelOverlay(tideui.SoftPanel{
		Prefix:  "tideftp",
		Title:   "about",
		Width:   width,
		Content: strings.TrimRight(m.about.Render(width, height), "\n"),
	})
	return &overlay
}
