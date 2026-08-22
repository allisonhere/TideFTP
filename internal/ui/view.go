package ui

import (
	"fmt"
	"strings"

	"github.com/allisonhere/tideui"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"tideftp/internal/domain"
)

func (m Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	renderer := tideui.NewRenderer(m.theme, tideui.StyleOptions{Density: m.density, PaneCorners: tideui.RoundCorners})
	topbar := m.renderTopbar(renderer)
	bodyHeight := max(1, m.height-2)
	topHeight, _ := m.layoutHeights()
	topHeight = min(topHeight, bodyHeight-3)
	bottomHeight := max(3, bodyHeight-topHeight)
	localWidth := max(20, int(float64(m.width)*m.fileSplit.Value()))
	remoteWidth := max(20, m.width-localWidth)

	local := m.renderPane(renderer, "Local", m.local.path, m.renderFilePane(renderer, m.local, localWidth-2, topHeight-2), localWidth, topHeight, m.focus == focusLocal)
	remote := m.renderPane(renderer, "Remote", m.remote.path, m.renderFilePane(renderer, m.remote, remoteWidth-2, topHeight-2), remoteWidth, topHeight, m.focus == focusRemote)
	top := lipgloss.JoinHorizontal(lipgloss.Top, local, remote)

	bottomTitle := "Transfers"
	bottomHint := m.bottomTabLabel()
	bottom := m.renderPane(renderer, bottomTitle, bottomHint, m.renderBottomPane(renderer, m.width-2, bottomHeight-2), m.width, bottomHeight, m.focus == focusQueue)
	main := lipgloss.JoinVertical(lipgloss.Left, top, bottom)
	status := m.renderStatus(renderer)
	view := lipgloss.JoinVertical(lipgloss.Left, topbar, main, status)

	if overlay := m.renderOverlay(renderer); overlay != nil && overlay.Visible {
		view = overlayOnBase(view, overlay.Content, m.width, m.height, renderer.Styles.Theme.Bg, m.shadow)
	}
	return clampView(view, m.width, m.height, renderer.Styles.Theme.Bg)
}

func (m Model) renderTopbar(renderer tideui.Renderer) string {
	left := renderer.Styles.StatusNotice.Render(" TideFTP ")
	conn := renderer.Styles.StatusBar.Render(" demo-sftp.local  sftp  fake adapter ")
	right := fmt.Sprintf(" %d queued  %s  split %.0f/%.0f ", countStatus(m.transfers, domain.Queued), m.theme.Name, m.fileSplit.Value()*100, (1-m.fileSplit.Value())*100)
	content := left + conn
	padding := max(0, m.width-lipgloss.Width(content)-len(right))
	return renderer.Styles.StatusBar.Width(m.width).Render(content + strings.Repeat(" ", padding) + right)
}

func (m Model) renderStatus(renderer tideui.Renderer) string {
	leftStyle := renderer.Styles.StatusSuccess
	if m.statusErr {
		leftStyle = renderer.Styles.StatusError
	}
	left := leftStyle.Render(" " + m.status + " ")
	right := "tab pane  enter open  space select  u upload  d download  c connect  t theme  shift+arrows resize  ? help  q quit"
	return renderer.Styles.StatusBar.Width(m.width).Render(align(left, right, m.width-2))
}

func (m Model) renderPane(renderer tideui.Renderer, title, hint, content string, width, height int, focused bool) string {
	width, height = max(1, width), max(1, height)
	headerStyle := renderer.Styles.PaneHeaderInactive
	if focused {
		headerStyle = renderer.Styles.PaneHeaderActive
	}
	innerW := max(1, width-2)
	innerH := max(1, height-2)
	header := headerStyle.Width(innerW).Render(align(title, hint, innerW))
	bodyHeight := max(0, innerH-1)
	body := clampView(content, innerW, bodyHeight, renderer.Styles.Theme.Bg)
	frame := renderer.Styles.PaneFrame(focused, "").Width(innerW).Height(innerH)
	return frame.Render(lipgloss.JoinVertical(lipgloss.Left, header, body))
}

func (m Model) renderFilePane(renderer tideui.Renderer, pane filePane, width, height int) string {
	width, height = max(1, width), max(1, height)
	rows := make([]string, 0, len(pane.entries)+1)
	rows = append(rows, renderer.Styles.DetailMeta.Width(width).Render("mode        size        modified        name"))
	visible := max(0, height-1)
	pane.offset = min(pane.offset, max(0, len(pane.entries)-visible))
	for index := pane.offset; index < len(pane.entries) && len(rows) < height; index++ {
		entry := pane.entries[index]
		prefix := entryIcon(entry) + " "
		if pane.selected[entry.Name] {
			prefix = "* " + prefix
		} else {
			prefix = "  " + prefix
		}
		size := formatSize(entry.Size)
		if entry.IsDir() {
			size = "<DIR>"
		}
		text := fmt.Sprintf("%-10s %9s  %-12s %s", short(entry.Mode, 10), size, entry.Modified.Format("Jan 02 15:04"), entry.Name)
		rows = append(rows, renderer.RenderRow(tideui.Row{
			Prefix:   prefix,
			Text:     text,
			Suffix:   "",
			Selected: index == pane.cursor,
			Muted:    entry.Hidden,
		}, width))
	}
	if len(pane.entries) == 0 {
		rows = append(rows, renderer.Styles.DetailMeta.Width(width).Render("empty"))
	}
	return strings.Join(rows, "\n")
}

func (m Model) renderBottomPane(renderer tideui.Renderer, width, height int) string {
	width, height = max(1, width), max(1, height)
	rows := []string{m.renderBottomTabs(renderer, width)}
	switch m.bottomTab {
	case tabQueue:
		rows = append(rows, m.renderTransferRows(renderer, width, height-1, func(t domain.Transfer) bool {
			return t.Status == domain.Queued || t.Status == domain.Active || t.Status == domain.Done
		})...)
	case tabActive:
		rows = append(rows, m.renderTransferRows(renderer, width, height-1, func(t domain.Transfer) bool { return t.Status == domain.Active })...)
	case tabFailed:
		rows = append(rows, m.renderTransferRows(renderer, width, height-1, func(t domain.Transfer) bool { return t.Status == domain.Failed })...)
	case tabHistory:
		rows = append(rows, m.renderTransferRows(renderer, width, height-1, func(t domain.Transfer) bool { return t.Status == domain.Done })...)
	case tabLog:
		for i := max(0, len(m.logs)-(height-1)); i < len(m.logs); i++ {
			rows = append(rows, renderer.Styles.DetailBody.Width(width).Render(m.logs[i]))
		}
	}
	if len(rows) == 1 {
		rows = append(rows, renderer.Styles.DetailMeta.Width(width).Render("no rows yet"))
	}
	return strings.Join(rows, "\n")
}

func (m Model) renderTransferRows(renderer tideui.Renderer, width, limit int, keep func(domain.Transfer) bool) []string {
	rows := make([]string, 0, limit)
	for _, transfer := range m.transfers {
		if !keep(transfer) || len(rows) >= limit {
			continue
		}
		dir := "↑"
		if transfer.Direction == domain.Download {
			dir = "↓"
		}
		bar := progressBar(transfer.Progress(), 18)
		name := short(transfer.Source+" -> "+transfer.Destination, max(12, width-48))
		meta := fmt.Sprintf("%3.0f%% %s", transfer.Progress()*100, transferStatus(transfer.Status))
		rows = append(rows, renderer.RenderRow(tideui.Row{
			Prefix: dir + " ",
			Text:   fmt.Sprintf("%s  %s", bar, name),
			Suffix: meta,
			Muted:  transfer.Status == domain.Done,
		}, width))
	}
	return rows
}

func (m Model) renderBottomTabs(renderer tideui.Renderer, width int) string {
	labels := []string{"1 Queue", "2 Active", "3 Failed", "4 History", "5 Log"}
	parts := make([]string, 0, len(labels))
	for index, label := range labels {
		style := renderer.Styles.DetailMeta
		if bottomTab(index) == m.bottomTab {
			style = renderer.Styles.DetailTitle
		}
		parts = append(parts, style.Render(" "+label+" "))
	}
	return renderer.Styles.DetailBody.Width(width).Render(strings.Join(parts, " "))
}

func (m Model) renderOverlay(renderer tideui.Renderer) *tideui.Overlay {
	switch m.overlay {
	case overlayHelp:
		keyRow := func(key, label string) string {
			return renderer.RenderSoftRow(tideui.SoftRow{Text: key, Suffix: label}, 64)
		}
		content := []string{
			renderer.Styles.DetailTitle.Render("Keyboard"),
			"",
			renderer.Styles.DetailMeta.Render("Navigate"),
			keyRow("tab / shift+tab", "switch panes"),
			keyRow("up/down, k/j", "move cursor"),
			keyRow("pgup / pgdown", "page up / down"),
			keyRow("enter", "open directory"),
			keyRow("backspace / h", "parent directory"),
			"",
			renderer.Styles.DetailMeta.Render("Act"),
			keyRow("space", "toggle selection"),
			keyRow("ctrl+a", "select all"),
			keyRow("esc", "clear selection / cancel"),
			keyRow("u", "upload"),
			keyRow("d", "download"),
			keyRow("r", "refresh"),
			keyRow(".", "toggle hidden files"),
			"",
			renderer.Styles.DetailMeta.Render("View"),
			keyRow("c", "connect"),
			keyRow("t", "theme picker"),
			keyRow("shift+left/right", "resize file panes"),
			keyRow("shift+up/down", "resize transfer pane"),
			keyRow("ctrl+0", "reset layout"),
			keyRow("1-5", "bottom tabs"),
			keyRow("q / ctrl+c", "quit"),
			"",
			renderer.RenderSoftHints(64, tideui.SoftHint{Key: "esc", Label: "close"}),
		}
		overlay := renderer.SoftPanelOverlay(tideui.SoftPanel{Prefix: "tideftp", Title: "help", Width: 70, Content: renderer.RenderSoftBody(70, strings.Join(content, "\n"))})
		return &overlay
	case overlayConnect:
		rows := []string{
			renderer.RenderSoftRow(tideui.SoftRow{Text: "Protocol", Suffix: "SFTP", Selected: true}, 60),
			renderer.RenderSoftRow(tideui.SoftRow{Text: "Host", Suffix: "demo-sftp.local"}, 60),
			renderer.RenderSoftRow(tideui.SoftRow{Text: "User", Suffix: "allie"}, 60),
			renderer.RenderSoftRow(tideui.SoftRow{Text: "Auth", Suffix: "prompt / agent / key file"}, 60),
			"",
			renderer.Styles.DetailMeta.Render("Profile preview: strict/ask/off known-host policy, ask by default."),
			renderer.Styles.DetailMeta.Render("Passwords are redacted and prompt by default."),
			"",
			renderer.RenderSoftHints(60, tideui.SoftHint{Key: "enter", Label: "connect fake"}, tideui.SoftHint{Key: "esc", Label: "cancel"}),
		}
		overlay := renderer.SoftPanelOverlay(tideui.SoftPanel{Prefix: "tideftp", Title: "connect", Width: 66, Content: renderer.RenderSoftBody(66, strings.Join(rows, "\n"))})
		return &overlay
	case overlayConflict:
		options := []string{"Overwrite", "Overwrite if source newer", "Overwrite if different size", "Overwrite if different size or source newer", "Resume", "Rename", "Skip"}
		rows := make([]string, 0, len(options)+4)
		for index, option := range options {
			rows = append(rows, renderer.RenderSoftRow(tideui.SoftRow{Text: option, Selected: index == len(options)-1}, 68))
		}
		rows = append(rows, "", renderer.Styles.DetailMeta.Render("Scope: this file / current queue / session"), renderer.RenderSoftHints(68, tideui.SoftHint{Key: "enter", Label: "simulate"}, tideui.SoftHint{Key: "esc", Label: "cancel"}))
		overlay := renderer.SoftPanelOverlay(tideui.SoftPanel{Prefix: "tideftp", Title: "file exists", Width: 74, Content: renderer.RenderSoftBody(74, strings.Join(rows, "\n"))})
		return &overlay
	case overlayTheme:
		overlay := m.themePicker.SoftModal(renderer, 42, m.height, "tideftp")
		return &overlay
	default:
		return nil
	}
}

func (m Model) layoutHeights() (int, int) {
	bodyHeight := max(1, m.height-2)
	bottom := max(3, int(float64(bodyHeight)*m.bottomSplit.Value()))
	top := max(5, bodyHeight-bottom)
	return top, 1 + top
}

func (m Model) bottomTabLabel() string {
	labels := []string{"queue", "active", "failed", "history", "log"}
	return labels[int(m.bottomTab)]
}

func entryIcon(entry domain.Entry) string {
	switch entry.Kind {
	case domain.EntryDir:
		return "▸"
	case domain.EntrySymlink:
		return "↪"
	default:
		return " "
	}
}

func formatSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}

func progressBar(progress float64, width int) string {
	progress = minFloat(maxFloat(progress, 0), 1)
	filled := int(progress * float64(width))
	return "[" + strings.Repeat("=", filled) + strings.Repeat(" ", max(0, width-filled)) + "]"
}

func transferStatus(status domain.TransferStatus) string {
	switch status {
	case domain.Queued:
		return "queued"
	case domain.Active:
		return "active"
	case domain.Failed:
		return "failed"
	case domain.Done:
		return "done"
	default:
		return "unknown"
	}
}

func countStatus(transfers []domain.Transfer, status domain.TransferStatus) int {
	count := 0
	for _, transfer := range transfers {
		if transfer.Status == status {
			count++
		}
	}
	return count
}

func overlayOnBase(base, box string, width, height int, bg lipgloss.Color, shadow bool) string {
	if shadow {
		box = addShadow(box, bg)
	}
	baseLines := strings.Split(clampView(base, width, height, bg), "\n")
	boxLines := strings.Split(box, "\n")
	boxWidth := 0
	for _, line := range boxLines {
		boxWidth = max(boxWidth, lipgloss.Width(line))
	}
	x := max(0, (width-boxWidth)/2)
	y := max(0, (height-len(boxLines))/2)
	for row, line := range boxLines {
		target := y + row
		if target < 0 || target >= len(baseLines) {
			continue
		}
		baseLines[target] = replaceAt(baseLines[target], line, x, width, bg)
	}
	return strings.Join(baseLines, "\n")
}

func addShadow(box string, bg lipgloss.Color) string {
	shadowStyle := lipgloss.NewStyle().Background(bg).Foreground(lipgloss.Color("#243946"))
	lines := strings.Split(box, "\n")
	for i := range lines {
		lines[i] += shadowStyle.Render("░")
	}
	if len(lines) > 0 {
		lines = append(lines, shadowStyle.Render(" "+strings.Repeat("░", max(0, lipgloss.Width(lines[0])-1))))
	}
	return strings.Join(lines, "\n")
}

func replaceAt(base, insert string, x, width int, bg lipgloss.Color) string {
	left := ansi.Truncate(base, x, "")
	rightStart := x + lipgloss.Width(insert)
	right := ""
	if rightStart < width {
		right = ansi.Truncate(base, width, "")
		if lipgloss.Width(right) > rightStart {
			right = ansi.Cut(right, rightStart, width)
		} else {
			right = lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", width-rightStart))
		}
	}
	return ansi.Truncate(left+insert+right, width, "")
}

func clampView(value string, width, height int, bg lipgloss.Color) string {
	width, height = max(1, width), max(1, height)
	style := lipgloss.NewStyle().Background(bg)
	lines := strings.Split(value, "\n")
	out := make([]string, height)
	for i := 0; i < height; i++ {
		if i < len(lines) {
			line := ansi.Truncate(lines[i], width, "")
			out[i] = line + style.Render(strings.Repeat(" ", max(0, width-lipgloss.Width(line))))
		} else {
			out[i] = style.Render(strings.Repeat(" ", width))
		}
	}
	return strings.Join(out, "\n")
}

func align(left, right string, width int) string {
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return left + strings.Repeat(" ", gap) + right
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
