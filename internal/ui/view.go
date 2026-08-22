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
	topHeight := m.topPaneHeight()
	bottomHeight := m.bottomPaneHeight()
	localWidth := max(20, int(float64(m.width)*m.fileSplit.Value()))
	remoteWidth := max(20, m.width-localWidth)

	// renderPane reserves 2 rows for its border and 1 for its title header,
	// so the content passed in must be sized to height-3 to fill exactly the
	// space actually visible inside the pane (see renderPane's bodyHeight).
	local := m.renderPane(renderer, m.paneTitle("Local", m.local), m.local.displayPath(), m.renderFilePane(renderer, m.local, localWidth-2, topHeight-3), localWidth, topHeight, m.focus == focusLocal)
	remote := m.renderPane(renderer, m.paneTitle("Remote", m.remote), m.remotePaneHint(), m.renderRemotePane(renderer, remoteWidth-2, topHeight-3), remoteWidth, topHeight, m.focus == focusRemote)
	top := lipgloss.JoinHorizontal(lipgloss.Top, local, remote)

	bottomTitle := "Transfers"
	bottomHint := m.bottomTabLabel()
	bottom := m.renderPane(renderer, bottomTitle, bottomHint, m.renderBottomPane(renderer, m.width-2, bottomHeight-3), m.width, bottomHeight, m.focus == focusQueue)
	main := lipgloss.JoinVertical(lipgloss.Left, top, bottom)
	status := m.renderStatus(renderer)
	view := lipgloss.JoinVertical(lipgloss.Left, topbar, main, status)

	if overlay := m.renderOverlay(renderer); overlay != nil && overlay.Visible {
		view = overlayOnBase(view, overlay.Content, m.width, m.height, renderer.Styles.Theme.Bg, m.shadow)
	}
	return clampView(view, m.width, m.height, renderer.Styles.Theme.Bg)
}

// connectionSummary is the topbar's account of the remote side.
func (m Model) connectionSummary() string {
	switch m.state {
	case connConnecting:
		return "connecting to " + m.target.Label() + "…"
	case connConnected:
		return m.target.Label() + "  " + m.target.Address()
	case connFailed:
		if m.connErr != nil {
			return "disconnected — " + m.connErr.Error()
		}
		return "disconnected"
	default:
		return "not connected — press c"
	}
}

// remotePaneHint is what the remote pane header shows to the right of its
// title: a directory when there is one, otherwise the connection state.
func (m Model) remotePaneHint() string {
	if m.connected() || m.remote.loading {
		return m.remote.displayPath()
	}
	return m.state.String()
}

// paneTitle appends a spinner-free loading marker while a listing is in
// flight. It stays in the title rather than replacing the body so the pane's
// current contents remain readable and usable during a slow listing.
func (m Model) paneTitle(title string, pane filePane) string {
	if !pane.loading {
		return title
	}
	return title + " " + m.glyphPlain("…", "...")
}

func (m Model) renderTopbar(renderer tideui.Renderer) string {
	left := renderer.Styles.StatusNotice.Render(" TideFTP ")
	conn := renderer.Styles.StatusBar.Render(" " + m.connectionSummary() + " ")
	right := fmt.Sprintf(" %d queued  %s  split %.0f/%.0f ", countStatus(m.transfers, domain.Queued), m.theme.Name, m.fileSplit.Value()*100, (1-m.fileSplit.Value())*100)
	content := left + conn
	padding := max(0, m.width-lipgloss.Width(content)-lipgloss.Width(right))
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
	// Render from a local offset: pane is a copy, so writing back to
	// pane.offset here would be silently discarded. clampCursors owns the
	// model's offset; this only guards against a stale one.
	offset := max(0, min(pane.offset, max(0, len(pane.entries)-visible)))
	for index := offset; index < len(pane.entries) && len(rows) < height; index++ {
		entry := pane.entries[index]
		rows = append(rows, m.renderEntryRow(renderer, entry, index == pane.cursor, pane.selected[entry.Name], width))
	}
	if len(pane.entries) == 0 {
		label := "empty"
		if pane.loading {
			label = "loading…"
		}
		rows = append(rows, renderer.Styles.DetailMeta.Width(width).Render(label))
	}
	return strings.Join(rows, "\n")
}

// renderRemotePane draws the remote listing, or an explanation of why there
// isn't one. A disconnected pane is not an empty directory and must not look
// like one.
func (m Model) renderRemotePane(renderer tideui.Renderer, width, height int) string {
	if m.connected() || m.remote.loading {
		return m.renderFilePane(renderer, m.remote, width, height)
	}
	lines := []string{""}
	switch m.state {
	case connConnecting:
		lines = append(lines, renderer.Styles.DetailTitle.Render("  Connecting to "+m.target.Label()+"…"))
	case connFailed:
		lines = append(lines, renderer.Styles.DetailTitle.Render("  Not connected"))
		if m.connErr != nil {
			lines = append(lines, "", renderer.Styles.StatusError.Render("  "+short(m.connErr.Error(), max(1, width-4))+"  "))
		}
		lines = append(lines, "", renderer.Styles.DetailMeta.Render("  Press c to pick a server and try again."))
	default:
		lines = append(lines, renderer.Styles.DetailTitle.Render("  Not connected"))
		lines = append(lines, "", renderer.Styles.DetailMeta.Render("  Press c to pick a server."))
	}
	return strings.Join(lines, "\n")
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
		visible := height - 1
		start := min(m.bottomOffset, max(0, len(m.logs)-visible))
		end := min(len(m.logs), start+visible)
		for i := start; i < end; i++ {
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
	skip := m.bottomOffset
	for _, transfer := range m.transfers {
		if !keep(transfer) {
			continue
		}
		if skip > 0 {
			skip--
			continue
		}
		if len(rows) >= limit {
			break
		}
		rows = append(rows, m.renderTransferRow(renderer, transfer, width))
	}
	return rows
}

// rowSurface resolves the background/foreground pair a row style paints,
// falling back to the theme's base colors if a style leaves either unset.
func rowSurface(renderer tideui.Renderer, style lipgloss.Style) (bg, fg lipgloss.Color) {
	bg, _ = style.GetBackground().(lipgloss.Color)
	fg, _ = style.GetForeground().(lipgloss.Color)
	if bg == "" {
		bg = renderer.Styles.Theme.Bg
	}
	if fg == "" {
		fg = renderer.Styles.Theme.Fg
	}
	return bg, fg
}

// segment renders text with an explicit background on every span so that
// concatenating differently-colored segments never lets one segment's
// trailing reset code erase the row's background for the segments after it.
func segment(bg, color lipgloss.Color, text string) string {
	return lipgloss.NewStyle().Background(bg).Foreground(color).Render(text)
}

// glyph returns unicodeGlyph unless icons are turned off or the active theme
// is ASCII-only (e.g. vt52), in which case it falls back to asciiGlyph.
func (m Model) glyph(renderer tideui.Renderer, unicodeGlyph, asciiGlyph string) string {
	if !m.showIcons || renderer.Styles.PlainUI {
		return asciiGlyph
	}
	return unicodeGlyph
}

// glyphPlain is glyph for callers that have no renderer to hand; it consults
// the icon toggle and the active theme's ASCII flag directly.
func (m Model) glyphPlain(unicodeGlyph, asciiGlyph string) string {
	if !m.showIcons || m.theme.UsesASCII() {
		return asciiGlyph
	}
	return unicodeGlyph
}

func (m Model) entryIcon(renderer tideui.Renderer, entry domain.Entry) string {
	switch entry.Kind {
	case domain.EntryDir:
		return m.glyph(renderer, "▸", ">")
	case domain.EntrySymlink:
		return m.glyph(renderer, "↪", "~")
	default:
		return " "
	}
}

func (m Model) renderEntryRow(renderer tideui.Renderer, entry domain.Entry, cursor, marked bool, width int) string {
	base := renderer.Styles.Item
	switch {
	case cursor:
		base = renderer.Styles.ItemSelected
	case marked:
		base = lipgloss.NewStyle().Background(renderer.Styles.Theme.Selected).Foreground(renderer.Styles.Theme.Fg)
	case entry.Hidden:
		base = renderer.Styles.ItemMuted
	}
	bg, fg := rowSurface(renderer, base)

	nameColor := fg
	if !entry.Hidden {
		switch entry.Kind {
		case domain.EntryDir:
			nameColor = renderer.Styles.Theme.BorderFocus
		case domain.EntrySymlink:
			nameColor = renderer.Styles.Theme.Unread
		}
	}

	mark := "  "
	if marked {
		mark = m.glyph(renderer, "✔", "*") + " "
	}
	size := formatSize(entry.Size)
	if entry.IsDir() {
		size = "<DIR>"
	}
	meta := fmt.Sprintf("%-10s %9s  %-12s ", short(entry.Mode, 10), size, entry.Modified.Format("Jan 02 15:04"))

	content := segment(bg, fg, mark) +
		segment(bg, nameColor, m.entryIcon(renderer, entry)+" ") +
		segment(bg, fg, meta) +
		segment(bg, nameColor, entry.Name)
	return clampView(content, width, 1, bg)
}

func (m Model) renderTransferRow(renderer tideui.Renderer, transfer domain.Transfer, width int) string {
	base := renderer.Styles.Item
	if transfer.Status == domain.Done {
		base = renderer.Styles.ItemMuted
	}
	bg, fg := rowSurface(renderer, base)

	statusColor := fg
	statusIcon := " "
	switch transfer.Status {
	case domain.Active:
		statusColor = renderer.Styles.Theme.BorderFocus
	case domain.Done:
		statusColor = renderer.Styles.Theme.Unread
		statusIcon = m.glyph(renderer, "✓", "+")
	case domain.Failed:
		statusColor = renderer.Styles.Theme.Error
		statusIcon = m.glyph(renderer, "✗", "x")
	case domain.Queued:
		statusColor = renderer.Styles.Theme.Dimmed
	}

	dir := m.glyph(renderer, "↑", "^")
	if transfer.Direction == domain.Download {
		dir = m.glyph(renderer, "↓", "v")
	}
	const barWidth = 18
	progress := min(max(transfer.Progress(), 0), 1)
	filled := int(progress * float64(barWidth))
	bar := segment(bg, fg, "[") +
		segment(bg, statusColor, strings.Repeat("=", filled)) +
		segment(bg, fg, strings.Repeat(" ", max(0, barWidth-filled))) +
		segment(bg, fg, "]")
	name := short(transfer.Source+" -> "+transfer.Destination, max(12, width-48))

	left := segment(bg, statusColor, dir+" ") + bar + segment(bg, fg, "  "+name)
	label := transfer.Message
	if label == "" {
		label = transferStatus(transfer.Status)
	}
	meta := fmt.Sprintf("%s %3.0f%% %s", statusIcon, transfer.Progress()*100, label)
	right := segment(bg, statusColor, meta)
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	content := left + segment(bg, fg, strings.Repeat(" ", gap)) + right
	return clampView(content, width, 1, bg)
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
			keyRow("x", "cancel active transfers"),
			keyRow("o", "conflict prompt (demo)"),
			keyRow(".", "toggle hidden files"),
			"",
			renderer.Styles.DetailMeta.Render("View"),
			keyRow("c", "connect / disconnect"),
			keyRow("t", "theme picker"),
			keyRow("i", "toggle icons"),
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
		rows := make([]string, 0, len(m.connectRows())+6)
		for index, row := range m.connectRows() {
			rows = append(rows, renderer.RenderSoftRow(tideui.SoftRow{Text: row.label, Selected: index == m.targetIndex}, 60))
		}
		if len(rows) == 0 {
			rows = append(rows, renderer.Styles.DetailMeta.Render("No connection profiles configured."))
		}
		rows = append(rows,
			"",
			renderer.Styles.DetailMeta.Render("Status: "+m.connectionSummary()),
			renderer.Styles.DetailMeta.Render("Passwords are redacted and prompt by default."),
			"",
			renderer.RenderSoftHints(60,
				tideui.SoftHint{Key: "up/down", Label: "choose"},
				tideui.SoftHint{Key: "enter", Label: "go"},
				tideui.SoftHint{Key: "esc", Label: "cancel"}),
		)
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

// topPaneHeight and bottomPaneHeight return the on-screen height (including
// border and title header) allocated to the local/remote panes and the
// transfers pane, respectively.
func (m Model) topPaneHeight() int {
	bodyHeight := max(1, m.height-2)
	bottom := max(3, int(float64(bodyHeight)*m.bottomSplit.Value()))
	return min(max(5, bodyHeight-bottom), bodyHeight-3)
}

func (m Model) bottomPaneHeight() int {
	bodyHeight := max(1, m.height-2)
	return max(3, bodyHeight-m.topPaneHeight())
}

// filePaneVisibleRows returns how many entry rows (excluding the column
// header) a local/remote pane can draw at the current terminal size. It is
// the window filePane.clamp scrolls the cursor within.
func (m Model) filePaneVisibleRows() int {
	return max(1, m.topPaneHeight()-4)
}

// bottomVisibleRows returns how many content rows (excluding the tab bar)
// are visible inside the transfers pane, i.e. how many rows renderBottomPane
// can actually show for the current terminal size.
func (m Model) bottomVisibleRows() int {
	bodyHeight := max(1, m.bottomPaneHeight()-3)
	return max(0, bodyHeight-1)
}

// bottomRowCount returns how many rows exist for the currently selected
// bottom-pane tab, regardless of how many are actually visible.
func (m Model) bottomRowCount() int {
	switch m.bottomTab {
	case tabQueue:
		count := 0
		for _, transfer := range m.transfers {
			if transfer.Status == domain.Queued || transfer.Status == domain.Active || transfer.Status == domain.Done {
				count++
			}
		}
		return count
	case tabActive:
		return countStatus(m.transfers, domain.Active)
	case tabFailed:
		return countStatus(m.transfers, domain.Failed)
	case tabHistory:
		return countStatus(m.transfers, domain.Done)
	case tabLog:
		return len(m.logs)
	default:
		return 0
	}
}

func (m Model) bottomTabLabel() string {
	labels := []string{"queue", "active", "failed", "history", "log"}
	return labels[int(m.bottomTab)]
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
