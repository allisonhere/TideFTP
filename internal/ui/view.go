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
	renderer := tideui.NewRenderer(m.theme, tideui.StyleOptions{Density: m.density, PaneCorners: tideui.RoundCorners, ModalShadow: m.shadow})
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
		view = renderer.OverlayModal(view, overlay.Content, m.width, m.height)
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

// fitRow renders text as exactly one row of the given width. Every row in the
// panes is laid out at a fixed width, and anything longer wraps onto a second
// line, pushing everything below it down — which is how a click lands on the
// wrong entry, and how the bottom pane's row count stops matching what it
// draws. Truncating is the fix; firstLine is the backstop.
func fitRow(style lipgloss.Style, width int, text string) string {
	return firstLine(style.Width(width).Render(short(text, width)))
}

// firstLine guards against a style wrapping content onto a second row, which
// would push every row below it down and break the mouse row mapping.
func firstLine(value string) string {
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		return value[:index]
	}
	return value
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
	// The StatusBar style pads one column on each side, so the line it is
	// given must be m.width-2. Laying out against m.width made the topbar
	// wrap onto a second row, which shifted every row below it.
	return renderer.Styles.StatusBar.Width(m.width).Render(align(left+conn, right, m.width-statusBarPadding))
}

func (m Model) renderStatus(renderer tideui.Renderer) string {
	leftStyle := renderer.Styles.StatusSuccess
	if m.statusErr {
		leftStyle = renderer.Styles.StatusError
	}
	left := leftStyle.Render(" " + m.status + " ")
	right := "tab pane  enter open  space select  u upload  d download  c connect  t theme  shift+arrows resize  ? help  q quit"
	return renderer.Styles.StatusBar.Width(m.width).Render(align(left, right, m.width-statusBarPadding))
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
	header = firstLine(header)
	bodyHeight := max(0, innerH-1)
	body := clampView(content, innerW, bodyHeight, renderer.Styles.Theme.Bg)
	frame := renderer.Styles.PaneFrame(focused, "").Width(innerW).Height(innerH)
	return frame.Render(lipgloss.JoinVertical(lipgloss.Left, header, body))
}

func (m Model) renderFilePane(renderer tideui.Renderer, pane filePane, width, height int) string {
	width, height = max(1, width), max(1, height)
	rows := make([]string, 0, len(pane.entries)+1)
	rows = append(rows, fitRow(renderer.Styles.DetailMeta, width, "mode        size        modified        name"))
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
		rows = append(rows, fitRow(renderer.Styles.DetailMeta, width, label))
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
			// The pane background, not the status bar's: a status-bar chip
			// dropped into the middle of a pane brings its own colours with it.
			bg := renderer.Styles.Theme.Bg
			lines = append(lines, "", segment(bg, readableOn(renderer.Styles.Theme.Error, bg, textMinContrast), "  "+short(m.connErr.Error(), max(1, width-4))))
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
	case tabLog:
		visible := height - 1
		start := min(m.bottomOffset, max(0, len(m.logs)-visible))
		end := min(len(m.logs), start+visible)
		for i := start; i < end; i++ {
			rows = append(rows, fitRow(renderer.Styles.DetailBody, width, m.logs[i]))
		}
	case tabStats:
		rows = append(rows, m.renderStatsTab(renderer, width, height-1)...)
	default:
		rows = append(rows, m.renderTransferRows(renderer, width, height-1)...)
	}
	if len(rows) == 1 {
		rows = append(rows, fitRow(renderer.Styles.DetailMeta, width, "no rows yet"))
	}
	return strings.Join(rows, "\n")
}

// renderTransferRows draws the current tab's rows starting at bottomOffset,
// highlighting bottomCursor's row when the queue pane has focus — the same
// windowing filePane rendering does over cursor/offset, but over
// bottomTabTransfers() instead of a filePane's entries.
func (m Model) renderTransferRows(renderer tideui.Renderer, width, limit int) []string {
	all := m.bottomTabTransfers()
	rows := make([]string, 0, limit)
	end := min(len(all), m.bottomOffset+limit)
	for i := m.bottomOffset; i < end; i++ {
		cursor := m.focus == focusQueue && i == m.bottomCursor
		rows = append(rows, m.renderTransferRow(renderer, all[i], cursor, width))
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

// entryPalette resolves the colours one file row is painted with. Every
// foreground is checked against the row's own background, which selection and
// marking both change; a colour that fails the check gives way to plain black
// or white, because a readable row matters more than a decorative one.
func entryPalette(renderer tideui.Renderer, entry domain.Entry, cursor, marked bool) (bg, fg, name lipgloss.Color) {
	base := renderer.Styles.Item
	switch {
	case cursor:
		base = renderer.Styles.ItemSelected
	case marked:
		base = lipgloss.NewStyle().Background(renderer.Styles.Theme.Selected).Foreground(renderer.Styles.Theme.Fg)
	case entry.Hidden:
		base = renderer.Styles.ItemMuted
	}
	bg, fg = rowSurface(renderer, base)

	// Hidden entries are meant to read as secondary, so they keep tideui's
	// lower floor for muted text rather than being forced to full contrast.
	floor := textMinContrast
	if entry.Hidden && !cursor && !marked {
		floor = dimMinContrast
	}
	fg = readableOn(fg, bg, floor)

	name = fg
	if !entry.Hidden {
		switch entry.Kind {
		case domain.EntryDir:
			name = readableOn(renderer.Styles.Theme.BorderFocus, bg, textMinContrast)
		case domain.EntrySymlink:
			name = readableOn(renderer.Styles.Theme.Unread, bg, textMinContrast)
		}
	}
	return bg, fg, name
}

func (m Model) renderEntryRow(renderer tideui.Renderer, entry domain.Entry, cursor, marked bool, width int) string {
	bg, fg, nameColor := entryPalette(renderer, entry, cursor, marked)

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

// transferPalette resolves the colours one transfer row is painted with, on
// the same terms as entryPalette: every foreground checked against the row's
// own background.
func transferPalette(renderer tideui.Renderer, status domain.TransferStatus, cursor bool) (bg, fg, accent lipgloss.Color) {
	base := renderer.Styles.Item
	switch {
	case cursor:
		base = renderer.Styles.ItemSelected
	case status == domain.Done:
		base = renderer.Styles.ItemMuted
	}
	bg, fg = rowSurface(renderer, base)

	floor := textMinContrast
	dimFloor := dimMinContrast
	if cursor {
		// The cursor row's background is much brighter than a plain row's;
		// an accent picked to merely clear the dim floor against a plain
		// background can fail outright against the selected one.
		floor = textMinContrast
		dimFloor = textMinContrast
	} else if status == domain.Done {
		floor = dimMinContrast
	}
	fg = readableOn(fg, bg, floor)

	accent = fg
	switch status {
	case domain.Active:
		accent = readableOn(renderer.Styles.Theme.BorderFocus, bg, textMinContrast)
	case domain.Done:
		accent = readableOn(renderer.Styles.Theme.Unread, bg, dimFloor)
	case domain.Failed, domain.Canceled:
		accent = readableOn(renderer.Styles.Theme.Error, bg, textMinContrast)
	case domain.Queued:
		accent = readableOn(renderer.Styles.Theme.Dimmed, bg, dimFloor)
	}
	return bg, fg, accent
}

func (m Model) renderTransferRow(renderer tideui.Renderer, transfer domain.Transfer, cursor bool, width int) string {
	bg, fg, statusColor := transferPalette(renderer, transfer.Status, cursor)

	statusIcon := " "
	switch transfer.Status {
	case domain.Done:
		statusIcon = m.glyph(renderer, "✓", "+")
	case domain.Failed:
		statusIcon = m.glyph(renderer, "✗", "x")
	case domain.Canceled:
		statusIcon = m.glyph(renderer, "⊘", "-")
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
	labels := []string{fmt.Sprintf("1 Queue (%dx)", m.maxParallel), "2 Active", "3 Failed", "4 History", "5 Log", "6 Stats"}
	parts := make([]string, 0, len(labels))
	for index, label := range labels {
		style := renderer.Styles.DetailMeta
		if bottomTab(index) == m.bottomTab {
			style = renderer.Styles.DetailTitle
		}
		parts = append(parts, style.Render(" "+label+" "))
	}
	return fitRow(renderer.Styles.DetailBody, width, strings.Join(parts, " "))
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
			keyRow("x", "cancel active transfer (queue pane) / all"),
			keyRow("R", "retry selected failed transfer"),
			keyRow("+/-", "more/fewer parallel transfers"),
			keyRow(".", "toggle hidden files"),
			"",
			renderer.Styles.DetailMeta.Render("View"),
			keyRow("c", "connect / disconnect"),
			keyRow("t", "theme picker"),
			keyRow(",", "settings"),
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
		width := min(76, max(60, m.width-8))
		contentWidth := width - 4
		if m.connectIdentityBrowse {
			browser := m.renderFilePane(renderer, m.connectIdentityPane, contentWidth, connectIdentityBrowserHeight)
			rows := []string{
				renderer.Styles.DetailMeta.Width(contentWidth).Render("Choose identity file  " + m.connectIdentityPane.displayPath()),
				"",
				browser,
				"",
				renderer.RenderSoftHints(contentWidth,
					tideui.SoftHint{Key: "up/down", Label: "select"},
					tideui.SoftHint{Key: "enter", Label: "open/use"},
					tideui.SoftHint{Key: "backspace/..", Label: "up"},
					tideui.SoftHint{Key: "esc", Label: "form"}),
			}
			content := renderer.RenderSoftBody(width, strings.Join(rows, "\n"))
			overlay := renderer.SoftPanelOverlay(tideui.SoftPanel{Prefix: "tideftp", Title: "choose identity file", Content: content, Width: width})
			return &overlay
		}
		formWidth := max(28, contentWidth)
		rows := make([]string, 0, 10)
		rows = append(rows, renderer.Styles.DetailMeta.Width(contentWidth).Render("Status  "+m.connectionSummary()))
		rows = append(rows, "")
		for field := connectFieldProfile; field < connectFieldCount; field++ {
			if !m.connectFieldVisible(field) {
				continue
			}
			rows = append(rows, renderer.RenderSoftRow(tideui.SoftRow{
				Text:     connectFieldLabel(field),
				Suffix:   m.connectFieldDisplay(field),
				Selected: field == m.connectField,
			}, formWidth))
		}
		if m.statusErr {
			rows = append(rows, "", renderer.Styles.StatusError.Width(contentWidth).Render(m.status))
		} else if m.connectFieldVisible(connectFieldPassword) {
			rows = append(rows, "", renderer.Styles.DetailMeta.Width(contentWidth).Render("Password can be left blank to fall back to the environment variable."))
		}
		actionHints := []tideui.SoftHint{
			{Key: "tab", Label: "next"},
			{Key: "h/l", Label: "change"},
			{Key: "ctrl/alt+enter", Label: "connect"},
			{Key: "ctrl+u", Label: "clear"},
		}
		if m.connectField == connectFieldIdentity && m.connectFieldVisible(connectFieldIdentity) {
			actionHints = append(actionHints, tideui.SoftHint{Key: "ctrl+b", Label: "local file"})
		}
		actionHints = append(actionHints, tideui.SoftHint{Key: "esc", Label: "cancel"})
		rows = append(rows, "",
			renderer.RenderSoftHints(contentWidth, actionHints...),
			renderer.RenderSoftHints(contentWidth,
				tideui.SoftHint{Key: "ctrl+s", Label: "save profile"},
				tideui.SoftHint{Key: "ctrl+x", Label: "delete profile"}),
		)
		if m.conn != nil {
			rows = append(rows, renderer.RenderSoftHints(contentWidth, tideui.SoftHint{Key: "ctrl+d", Label: "disconnect"}))
		}
		content := renderer.RenderSoftBody(width, strings.Join(rows, "\n"))
		overlay := renderer.SoftPanelOverlay(tideui.SoftPanel{Prefix: "tideftp", Title: "connect", Content: content, Width: width})
		return &overlay
	case overlayConflict:
		if m.preflight == nil {
			return nil
		}
		scan := m.preflight
		idx := scan.currentConflictIndex()
		if idx < 0 {
			return nil
		}
		current := scan.files[idx]
		summary := fmt.Sprintf("file %d of %d: %s — %s here, %s at destination",
			scan.resolvedConflictCount()+1, scan.conflictCount(), current.name, formatSize(current.size), formatSize(current.conflict.Size))
		rows := make([]string, 0, int(conflictPolicyCount)+4)
		rows = append(rows, renderer.Styles.DetailBody.Width(68).Render(summary), "")
		for p := conflictPolicy(0); p < conflictPolicyCount; p++ {
			rows = append(rows, renderer.RenderSoftRow(tideui.SoftRow{Text: conflictPolicyLabel(p), Selected: int(p) == scan.cursor}, 68))
		}
		rows = append(rows, "", renderer.RenderSoftHints(68,
			tideui.SoftHint{Key: "↑↓", Label: "select"},
			tideui.SoftHint{Key: "enter", Label: "this file"},
			tideui.SoftHint{Key: "a", Label: "all"},
			tideui.SoftHint{Key: "s", Label: "all+remember"},
			tideui.SoftHint{Key: "esc", Label: "cancel"}))
		overlay := renderer.SoftPanelOverlay(tideui.SoftPanel{Prefix: "tideftp", Title: "file exists", Width: 74, Content: renderer.RenderSoftBody(74, strings.Join(rows, "\n"))})
		return &overlay
	case overlayTheme:
		overlay := m.themePicker.SoftModal(renderer, 42, m.height, "tideftp")
		return &overlay
	case overlayPreflight:
		if m.preflight == nil {
			return nil
		}
		scan := m.preflight
		dir := "upload"
		if scan.direction == domain.Download {
			dir = "download"
		}
		summary := fmt.Sprintf("%s %d file(s) across %d folder(s), %s total", dir, len(scan.files), scan.folders, formatSize(scan.totalBytes))
		if scan.truncated {
			summary += " (stopped early — more than that were found)"
		}
		rows := []string{
			renderer.Styles.DetailBody.Width(64).Render(summary),
			"",
			renderer.RenderSoftHints(64, tideui.SoftHint{Key: "enter", Label: "queue"}, tideui.SoftHint{Key: "esc", Label: "cancel"}),
		}
		overlay := renderer.SoftPanelOverlay(tideui.SoftPanel{Prefix: "tideftp", Title: "queue folder", Width: 70, Content: renderer.RenderSoftBody(70, strings.Join(rows, "\n"))})
		return &overlay
	case overlayHostKey:
		if m.hostKeyPrompt == nil {
			return nil
		}
		prompt := m.hostKeyPrompt
		rows := []string{
			renderer.Styles.DetailBody.Width(64).Render("Unknown host key for " + prompt.target.Address()),
			renderer.Styles.DetailMeta.Width(64).Render(prompt.err.Algorithm + " " + prompt.err.Fingerprint),
			"",
			renderer.RenderSoftHints(64,
				tideui.SoftHint{Key: "y", Label: "trust once"},
				tideui.SoftHint{Key: "r", Label: "trust & remember"},
				tideui.SoftHint{Key: "n", Label: "cancel"}),
		}
		overlay := renderer.SoftPanelOverlay(tideui.SoftPanel{Prefix: "tideftp", Title: "unknown host key", Width: 70, Content: renderer.RenderSoftBody(70, strings.Join(rows, "\n"))})
		return &overlay
	case overlayCommandPalette:
		width := min(72, max(48, m.width-8))
		contentWidth := width - 4
		query := m.commandQuery
		if query == "" {
			query = "|"
		} else {
			query += "|"
		}
		commands := m.filteredPaletteCommands()
		rows := []string{
			renderer.Styles.DetailMeta.Width(contentWidth).Render("Search  " + query),
			"",
		}
		if len(commands) == 0 {
			rows = append(rows, renderer.Styles.DetailMeta.Width(contentWidth).Render("No matching commands"))
		}
		for i := 0; i < len(commands); i++ {
			command := commands[i]
			rows = append(rows, renderer.RenderSoftRow(tideui.SoftRow{
				Text:     command.title,
				Suffix:   command.hint,
				Selected: i == m.commandCursor,
			}, contentWidth))
		}
		rows = append(rows, "", renderer.RenderSoftHints(contentWidth,
			tideui.SoftHint{Key: "type", Label: "filter"},
			tideui.SoftHint{Key: "up/down", Label: "select"},
			tideui.SoftHint{Key: "enter", Label: "run"},
			tideui.SoftHint{Key: "esc", Label: "close"},
		))
		overlay := renderer.SoftPanelOverlay(tideui.SoftPanel{Prefix: "tideftp", Title: "command palette", Width: width, Content: renderer.RenderSoftBody(width, strings.Join(rows, "\n"))})
		return &overlay
	case overlaySettings:
		width := min(60, max(36, m.width-8))
		contentWidth := width - 4
		rows := make([]string, 0, int(settingsFieldCount)+2)
		for field := settingsField(0); field < settingsFieldCount; field++ {
			rows = append(rows, renderer.RenderSoftRow(tideui.SoftRow{
				Text:     settingsFieldLabel(field),
				Suffix:   m.settingsFieldValue(field),
				Selected: int(field) == m.settingsCursor,
			}, contentWidth))
		}
		rows = append(rows, "", renderer.RenderSoftHints(contentWidth,
			tideui.SoftHint{Key: "up/down", Label: "select"},
			tideui.SoftHint{Key: "h/l", Label: "change"},
			tideui.SoftHint{Key: "esc", Label: "close"},
		))
		overlay := renderer.SoftPanelOverlay(tideui.SoftPanel{Prefix: "tideftp", Title: "settings", Width: width, Content: renderer.RenderSoftBody(width, strings.Join(rows, "\n"))})
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
	case tabLog:
		return len(m.logs)
	case tabStats:
		return 0 // a fixed live view, nothing to scroll
	default:
		return len(m.bottomTabTransfers())
	}
}

func (m Model) bottomTabLabel() string {
	labels := []string{"queue", "active", "failed", "history", "log", "stats"}
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
	case domain.Canceled:
		return "canceled"
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

// statusBarPadding is the horizontal padding tideui's StatusBar style adds,
// one column per side.
const statusBarPadding = 2

// align lays left and right out on a single line of exactly width columns.
//
// It must never return more than width: every caller renders the result into
// a fixed-width style, and an over-long line wraps onto a second row. For the
// pane headers and the status bars that silently shifts everything below them
// down, which is how mouse clicks end up on the wrong row. The left side wins
// when there is not enough space, since it holds the title.
func align(left, right string, width int) string {
	width = max(1, width)
	left = ansi.Truncate(left, width, "")
	room := width - lipgloss.Width(left) - 1
	if room <= 0 {
		return left
	}
	if lipgloss.Width(right) > room {
		right = short(right, room)
	}
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return ansi.Truncate(left+strings.Repeat(" ", gap)+right, width, "")
}
