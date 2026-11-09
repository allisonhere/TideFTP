package ui

import (
	"fmt"
	"strings"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"
)

// Settings → Changelog: a read-only, scrollable view of CHANGELOG.md. The
// text is embedded into the binary by main (see SetChangelog), so it always
// matches the running version and works offline.

var changelogSource string

// SetChangelog supplies the CHANGELOG.md text shown in Settings → Changelog.
func SetChangelog(markdown string) { changelogSource = markdown }

// changelogPanelWidth matches the help and About modals.
const changelogPanelWidth = 70

type changelogKind int

const (
	changelogRelease changelogKind = iota
	changelogGroup
	changelogItem
)

type changelogBlock struct {
	kind changelogKind
	text string
}

// parseChangelog flattens the Keep-a-Changelog markdown into release
// headings, group headings, and bullet items. The preamble before the first
// "## " heading is dropped, and releases with no entries (an empty
// "## Unreleased") are omitted.
func parseChangelog(src string) []changelogBlock {
	var blocks []changelogBlock
	started := false
	for _, raw := range strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(raw, " \t")
		switch {
		case strings.HasPrefix(line, "## "):
			started = true
			blocks = append(blocks, changelogBlock{changelogRelease, cleanChangelogInline(line[3:])})
		case !started, strings.TrimSpace(line) == "":
		case strings.HasPrefix(line, "### "):
			blocks = append(blocks, changelogBlock{changelogGroup, cleanChangelogInline(line[4:])})
		case strings.HasPrefix(line, "- "):
			blocks = append(blocks, changelogBlock{changelogItem, cleanChangelogInline(line[2:])})
		case len(blocks) > 0 && blocks[len(blocks)-1].kind == changelogItem && strings.HasPrefix(line, " "):
			last := &blocks[len(blocks)-1]
			last.text += " " + cleanChangelogInline(strings.TrimSpace(line))
		}
	}
	out := blocks[:0:0]
	for i, b := range blocks {
		if b.kind == changelogRelease && (i+1 == len(blocks) || blocks[i+1].kind == changelogRelease) {
			continue
		}
		out = append(out, b)
	}
	return out
}

func cleanChangelogInline(s string) string {
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "`", "")
	return strings.TrimSpace(s)
}

// changelogLines renders the parsed changelog as styled, width-fitted lines
// ready to window for scrolling.
func changelogLines(renderer tideui.Renderer, width int) []string {
	blocks := parseChangelog(changelogSource)
	if len(blocks) == 0 {
		return []string{renderer.Styles.DetailMeta.Width(width).Render("No changelog is bundled with this build.")}
	}
	var lines []string
	for _, b := range blocks {
		switch b.kind {
		case changelogRelease:
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, renderer.Styles.DetailTitle.Render(b.text))
		case changelogGroup:
			lines = append(lines, "", renderer.Styles.DetailMeta.Render("  "+b.text))
		case changelogItem:
			wrapped := strings.Split(renderer.Styles.DetailBody.Width(max(1, width-6)).Render(b.text), "\n")
			for i, part := range wrapped {
				prefix := "      "
				if i == 0 {
					prefix = "    • "
				}
				lines = append(lines, prefix+strings.TrimRight(part, " "))
			}
		}
	}
	return lines
}

// openChangelog shows the changelog from the top. Closing it returns to
// wherever it was opened from — Settings or the main view.
func (m *Model) openChangelog() {
	m.changelogReturn = m.overlay
	if m.changelogReturn != overlaySettings {
		m.changelogReturn = overlayNone
	}
	m.changelogOffset = 0
	m.overlay = overlayChangelog
	m.setStatus("changelog")
}

func (m Model) changelogVisibleRows() int {
	return max(5, min(24, m.height-10))
}

func (m Model) changelogTotalRows() int {
	return len(changelogLines(tideui.NewRenderer(m.theme, tideui.StyleOptions{Density: m.density}), changelogPanelWidth-6))
}

func (m *Model) clampChangelogOffset() {
	m.changelogOffset = min(max(0, m.changelogOffset), max(0, m.changelogTotalRows()-m.changelogVisibleRows()))
}

func (m *Model) handleChangelogKey(msg tea.KeyMsg) tea.Cmd {
	page := m.changelogVisibleRows()
	switch msg.String() {
	case "esc", "q":
		m.overlay = m.changelogReturn
		return nil
	case "up", "k":
		m.changelogOffset--
	case "down", "j":
		m.changelogOffset++
	case "pgup", "ctrl+u":
		m.changelogOffset -= page
	case "pgdown", "ctrl+d", " ":
		m.changelogOffset += page
	case "home", "g":
		m.changelogOffset = 0
	case "end", "G":
		m.changelogOffset = 1 << 30
	}
	m.clampChangelogOffset()
	return nil
}

func (m Model) renderChangelogOverlay(renderer tideui.Renderer) *tideui.Overlay {
	contentWidth := changelogPanelWidth - 6
	lines := changelogLines(renderer, contentWidth)
	visible := m.changelogVisibleRows()
	offset := min(max(0, m.changelogOffset), max(0, len(lines)-visible))
	end := min(len(lines), offset+visible)
	content := append([]string{}, lines[offset:end]...)
	for len(content) < visible && len(lines) > visible {
		content = append(content, "")
	}
	back := "close"
	if m.changelogReturn == overlaySettings {
		back = "back"
	}
	content = append(content, "",
		renderer.Styles.DetailMeta.Width(contentWidth).Render(fmt.Sprintf("Rows  %d-%d/%d", min(len(lines), offset+1), end, len(lines))),
		renderer.RenderSoftHints(contentWidth,
			tideui.SoftHint{Key: "j/k", Label: "scroll"},
			tideui.SoftHint{Key: "pgup/pgdn", Label: "page"},
			tideui.SoftHint{Key: "home/end", Label: "top/bottom"},
			tideui.SoftHint{Key: "esc", Label: back}))
	overlay := renderer.SoftPanelOverlay(tideui.SoftPanel{
		Prefix:  "tideftp",
		Title:   "changelog",
		Width:   changelogPanelWidth,
		Content: renderer.RenderSoftBody(changelogPanelWidth, strings.Join(content, "\n")),
	})
	return &overlay
}
