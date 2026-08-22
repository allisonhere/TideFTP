package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"tideftp/internal/domain"
	"tideftp/internal/remotefs"
)

type focusPane int

const (
	focusLocal focusPane = iota
	focusRemote
	focusQueue
)

type bottomTab int

const (
	tabQueue bottomTab = iota
	tabActive
	tabFailed
	tabHistory
	tabLog
)

type overlayMode int

const (
	overlayNone overlayMode = iota
	overlayHelp
	overlayConnect
	overlayConflict
	overlayTheme
)

type filePane struct {
	title      string
	path       string
	entries    []domain.Entry
	cursor     int
	offset     int
	showHidden bool
	selected   map[string]bool
}

type Model struct {
	width, height int
	focus         focusPane
	overlay       overlayMode

	local    filePane
	remote   filePane
	remoteFS remotefs.FS

	transfers      []domain.Transfer
	nextTransferID int
	bottomTab      bottomTab
	logs           []string

	theme       tideui.Theme
	themePicker tideui.ThemePicker
	density     tideui.Density
	shadow      bool

	fileSplit   tideui.PaneRatio
	bottomSplit tideui.PaneRatio

	status    string
	statusErr bool
}

type transferTick struct{}

func NewModel(remote remotefs.FS) Model {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	model := Model{
		focus: focusLocal,
		local: filePane{
			title:      "Local",
			path:       cwd,
			selected:   map[string]bool{},
			showHidden: false,
		},
		remote: filePane{
			title:      "Remote",
			path:       "/public_html",
			selected:   map[string]bool{},
			showHidden: false,
		},
		remoteFS:       remote,
		nextTransferID: 1,
		theme:          tideNight,
		density:        tideui.Compact,
		shadow:         true,
		fileSplit:      tideui.NewPaneRatio(tideui.PaneRatioOptions{Initial: 0.5, Min: 0.25, Max: 0.75, Step: 0.03}),
		bottomSplit:    tideui.NewPaneRatio(tideui.PaneRatioOptions{Initial: 0.28, Min: 0.15, Max: 0.50, Step: 0.03}),
		logs:           []string{"redacted logs enabled", "fake protocol adapter online", "profiles: demo ftp / demo ftps / demo sftp"},
		status:         "fake adapter ready",
	}
	model.themePicker = tideui.NewThemePicker(tideui.ThemePickerOptions{
		Themes:       appThemes(),
		InitialTheme: model.theme.Name,
		Title:        "THEMES",
	})
	model.refreshLocal()
	model.refreshRemote()
	return model
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(tickTransfers(), tea.SetWindowTitle("TideFTP"))
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case transferTick:
		m.advanceTransfers()
		return m, tickTransfers()
	case tea.MouseMsg:
		return m.updateMouse(msg)
	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.overlay == overlayTheme {
		action := m.themePicker.Update(msg)
		m.theme = m.themePicker.PreviewTheme()
		switch action {
		case tideui.ThemePickerConfirm:
			m.overlay = overlayNone
			m.theme = m.themePicker.ConfirmedTheme()
			m.setStatus("theme set to " + m.theme.Name)
		case tideui.ThemePickerCancel:
			m.overlay = overlayNone
			m.theme = m.themePicker.ConfirmedTheme()
			m.setStatus("theme unchanged")
		}
		return m, nil
	}
	if m.overlay != overlayNone {
		switch msg.String() {
		case "esc", "q", "n":
			m.overlay = overlayNone
			m.setStatus("cancelled")
		case "enter", "y":
			switch m.overlay {
			case overlayConnect:
				m.overlay = overlayNone
				m.setStatus("connected to demo-sftp.local using fake adapter")
				m.logs = append(m.logs, "connect demo-sftp.local:22 as allie (credentials redacted)")
			case overlayConflict:
				m.overlay = overlayNone
				m.queueFocusedTransfer()
			case overlayHelp:
				m.overlay = overlayNone
			}
		}
		return m, nil
	}

	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "tab":
		m.focus = (m.focus + 1) % 3
	case "shift+tab":
		m.focus = (m.focus + 2) % 3
	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
	case "pgup":
		m.moveCursor(-10)
	case "pgdown":
		m.moveCursor(10)
	case "enter":
		m.activateCursor()
	case "backspace", "h":
		m.parentDir()
	case " ":
		m.toggleSelection()
	case "esc":
		m.clearSelection()
	case "ctrl+a":
		m.selectAll()
	case ".":
		m.toggleHidden()
	case "u":
		m.queueUpload()
	case "d":
		m.queueDownload()
	case "delete":
		m.overlay = overlayConflict
	case "r":
		m.refresh()
	case "c":
		m.overlay = overlayConnect
	case "t":
		m.overlay = overlayTheme
		m.themePicker.Open(m.theme.Name)
	case "?":
		m.overlay = overlayHelp
	case "shift+left":
		m.fileSplit.Shrink()
		m.setStatus("local pane narrower")
	case "shift+right":
		m.fileSplit.Grow()
		m.setStatus("local pane wider")
	case "shift+up":
		m.bottomSplit.Grow()
		m.setStatus("transfer pane taller")
	case "shift+down":
		m.bottomSplit.Shrink()
		m.setStatus("transfer pane shorter")
	case "ctrl+0":
		m.fileSplit = tideui.NewPaneRatio(tideui.PaneRatioOptions{Initial: 0.5, Min: 0.25, Max: 0.75, Step: 0.03})
		m.bottomSplit = tideui.NewPaneRatio(tideui.PaneRatioOptions{Initial: 0.28, Min: 0.15, Max: 0.50, Step: 0.03})
		m.setStatus("layout reset")
	case "1":
		m.bottomTab = tabQueue
	case "2":
		m.bottomTab = tabActive
	case "3":
		m.bottomTab = tabFailed
	case "4":
		m.bottomTab = tabHistory
	case "5":
		m.bottomTab = tabLog
	}
	m.clampCursors()
	return m, nil
}

func (m Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress && msg.Action != tea.MouseActionMotion {
		return m, nil
	}
	topHeight, bottomStart := m.layoutHeights()
	localWidth := max(20, int(float64(m.width)*m.fileSplit.Value()))
	switch {
	case msg.Y < topHeight && msg.X < localWidth:
		m.focus = focusLocal
		m.cursorFromMouse(&m.local, msg.Y)
	case msg.Y < topHeight:
		m.focus = focusRemote
		m.cursorFromMouse(&m.remote, msg.Y)
	case msg.Y >= bottomStart:
		m.focus = focusQueue
	}
	return m, nil
}

func (m *Model) refresh() {
	m.refreshLocal()
	m.refreshRemote()
	m.setStatus("refreshed")
}

func (m *Model) refreshLocal() {
	entries, err := listLocal(m.local.path, m.local.showHidden)
	if err != nil {
		m.setError(err.Error())
		return
	}
	m.local.entries = entries
	m.local.cursor = min(m.local.cursor, max(0, len(entries)-1))
}

func (m *Model) refreshRemote() {
	m.remote.entries = m.remoteFS.List(m.remote.path, m.remote.showHidden)
	m.remote.cursor = min(m.remote.cursor, max(0, len(m.remote.entries)-1))
}

func (m *Model) moveCursor(delta int) {
	switch m.focus {
	case focusLocal:
		m.local.cursor += delta
	case focusRemote:
		m.remote.cursor += delta
	case focusQueue:
		if delta > 0 {
			m.bottomTab = minTab(m.bottomTab + 1)
		} else if delta < 0 {
			m.bottomTab = maxTab(m.bottomTab - 1)
		}
	}
}

func (m *Model) activateCursor() {
	switch m.focus {
	case focusLocal:
		entry, ok := m.local.current()
		if !ok || !entry.IsDir() {
			return
		}
		m.local.path = filepath.Join(m.local.path, entry.Name)
		m.local.cursor = 0
		m.refreshLocal()
	case focusRemote:
		entry, ok := m.remote.current()
		if !ok || !entry.IsDir() {
			return
		}
		m.remote.path = m.remoteFS.Child(m.remote.path, entry.Name)
		m.remote.cursor = 0
		m.refreshRemote()
	}
}

func (m *Model) parentDir() {
	switch m.focus {
	case focusLocal:
		parent := filepath.Dir(m.local.path)
		if parent != m.local.path {
			m.local.path = parent
			m.local.cursor = 0
			m.refreshLocal()
		}
	case focusRemote:
		m.remote.path = m.remoteFS.Parent(m.remote.path)
		m.remote.cursor = 0
		m.refreshRemote()
	}
}

func (m *Model) toggleSelection() {
	pane := m.focusedFilePane()
	if pane == nil {
		return
	}
	entry, ok := pane.current()
	if !ok {
		return
	}
	pane.selected[entry.Name] = !pane.selected[entry.Name]
	if !pane.selected[entry.Name] {
		delete(pane.selected, entry.Name)
	}
}

func (m *Model) clearSelection() {
	if pane := m.focusedFilePane(); pane != nil {
		pane.selected = map[string]bool{}
		m.setStatus("selection cleared")
	}
}

func (m *Model) selectAll() {
	if pane := m.focusedFilePane(); pane != nil {
		for _, entry := range pane.entries {
			pane.selected[entry.Name] = true
		}
		m.setStatus(fmt.Sprintf("selected %d item(s)", len(pane.selected)))
	}
}

func (m *Model) toggleHidden() {
	if pane := m.focusedFilePane(); pane != nil {
		pane.showHidden = !pane.showHidden
		m.refreshLocal()
		m.refreshRemote()
		m.setStatus("hidden files toggled")
	}
}

func (m *Model) queueUpload() {
	if len(m.local.actionEntries()) == 0 {
		m.setError("nothing selected or highlighted locally")
		return
	}
	m.queueTransfer(domain.Upload)
}

func (m *Model) queueDownload() {
	if len(m.remote.actionEntries()) == 0 {
		m.setError("nothing selected or highlighted remotely")
		return
	}
	m.queueTransfer(domain.Download)
}

func (m *Model) queueFocusedTransfer() {
	if m.focus == focusRemote {
		m.queueTransfer(domain.Download)
	} else {
		m.queueTransfer(domain.Upload)
	}
}

func (m *Model) queueTransfer(direction domain.TransferDirection) {
	var entries []domain.Entry
	var srcBase, dstBase string
	if direction == domain.Download {
		entries = m.remote.actionEntries()
		srcBase = m.remote.path
		dstBase = m.local.path
	} else {
		entries = m.local.actionEntries()
		srcBase = m.local.path
		dstBase = m.remote.path
	}
	if len(entries) == 0 {
		return
	}
	for _, entry := range entries {
		total := entry.Size
		if entry.IsDir() {
			total = 3_200_000
		}
		m.transfers = append(m.transfers, domain.Transfer{
			ID:          m.nextTransferID,
			Direction:   direction,
			Source:      joinDisplayPath(srcBase, entry.Name),
			Destination: joinDisplayPath(dstBase, entry.Name),
			BytesTotal:  max64(total, 64_000),
			Status:      domain.Queued,
			Message:     "queued",
		})
		m.nextTransferID++
	}
	m.setStatus(fmt.Sprintf("queued %d transfer(s)", len(entries)))
}

func (m *Model) advanceTransfers() {
	active := 0
	for i := range m.transfers {
		if m.transfers[i].Status == domain.Active {
			active++
		}
	}
	for i := range m.transfers {
		if active >= 2 {
			break
		}
		if m.transfers[i].Status == domain.Queued {
			m.transfers[i].Status = domain.Active
			m.transfers[i].StartedAt = time.Now()
			m.transfers[i].Message = "transferring"
			active++
		}
	}
	for i := range m.transfers {
		if m.transfers[i].Status != domain.Active {
			continue
		}
		step := max64(48_000, m.transfers[i].BytesTotal/12)
		m.transfers[i].BytesDone += step
		if m.transfers[i].BytesDone >= m.transfers[i].BytesTotal {
			m.transfers[i].BytesDone = m.transfers[i].BytesTotal
			m.transfers[i].Status = domain.Done
			m.transfers[i].FinishedAt = time.Now()
			m.transfers[i].Message = "complete"
		}
	}
}

func (m *Model) focusedFilePane() *filePane {
	switch m.focus {
	case focusLocal:
		return &m.local
	case focusRemote:
		return &m.remote
	default:
		return nil
	}
}

func (m *Model) clampCursors() {
	m.local.clamp()
	m.remote.clamp()
}

func (m *Model) cursorFromMouse(pane *filePane, y int) {
	row := y - 5
	if row < 0 {
		return
	}
	pane.cursor = pane.offset + row
	pane.clamp()
}

func (m *Model) setStatus(value string) {
	m.status, m.statusErr = value, false
}

func (m *Model) setError(value string) {
	m.status, m.statusErr = value, true
	m.logs = append(m.logs, "error: "+value)
}

func (p *filePane) current() (domain.Entry, bool) {
	if p.cursor < 0 || p.cursor >= len(p.entries) {
		return domain.Entry{}, false
	}
	return p.entries[p.cursor], true
}

func (p *filePane) clamp() {
	p.cursor = min(max(0, p.cursor), max(0, len(p.entries)-1))
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+20 {
		p.offset = max(0, p.cursor-19)
	}
}

func (p filePane) actionEntries() []domain.Entry {
	if len(p.selected) == 0 {
		entry, ok := p.current()
		if !ok {
			return nil
		}
		return []domain.Entry{entry}
	}
	entries := make([]domain.Entry, 0, len(p.selected))
	for _, entry := range p.entries {
		if p.selected[entry.Name] {
			entries = append(entries, entry)
		}
	}
	return entries
}

func listLocal(dirPath string, showHidden bool) ([]domain.Entry, error) {
	items, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}
	entries := make([]domain.Entry, 0, len(items))
	for _, item := range items {
		if !showHidden && strings.HasPrefix(item.Name(), ".") {
			continue
		}
		info, err := item.Info()
		if err != nil {
			continue
		}
		kind := domain.EntryFile
		mode := info.Mode().String()
		if item.IsDir() {
			kind = domain.EntryDir
		} else if info.Mode()&os.ModeSymlink != 0 {
			kind = domain.EntrySymlink
		}
		entries = append(entries, domain.Entry{Name: item.Name(), Kind: kind, Size: info.Size(), Mode: mode, Modified: info.ModTime(), Hidden: strings.HasPrefix(item.Name(), ".")})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind == domain.EntryDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, nil
}

func tickTransfers() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return transferTick{} })
}

func minTab(tab bottomTab) bottomTab {
	if tab > tabLog {
		return tabLog
	}
	return tab
}

func maxTab(tab bottomTab) bottomTab {
	if tab < tabQueue {
		return tabQueue
	}
	return tab
}

func joinDisplayPath(base, name string) string {
	if strings.HasPrefix(base, "/") {
		return strings.TrimRight(base, "/") + "/" + name
	}
	return filepath.Join(base, name)
}

func short(value string, width int) string {
	return ansi.Truncate(value, max(1, width), "…")
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
