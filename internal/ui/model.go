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
	"tideftp/internal/transfer"
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
	engine   transfer.Engine

	transfers      []domain.Transfer
	nextTransferID int
	maxParallel    int
	bottomTab      bottomTab
	bottomOffset   int
	logs           []string

	theme       tideui.Theme
	themePicker tideui.ThemePicker
	density     tideui.Density
	shadow      bool
	showIcons   bool

	fileSplit   tideui.PaneRatio
	bottomSplit tideui.PaneRatio

	status    string
	statusErr bool
}

// transferStreamClosed arrives when the engine has shut down and will send no
// more events, so the UI stops waiting on the channel.
type transferStreamClosed struct{}

// defaultParallelTransfers is how many transfers run at once until config
// persistence lands and can override Model.maxParallel.
const defaultParallelTransfers = 2

// firstFileRow is the screen row of the first entry in a file pane:
// topbar (1) + pane top border (1) + pane title header (1) + column
// header (1). Mouse clicks above it are not on an entry.
const firstFileRow = 4

func NewModel(remote remotefs.FS, engine transfer.Engine) Model {
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
		engine:         engine,
		nextTransferID: 1,
		maxParallel:    defaultParallelTransfers,
		theme:          tideNight,
		density:        tideui.Compact,
		shadow:         true,
		showIcons:      true,
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
	return tea.Batch(waitForTransferEvent(m.engine.Events()), tea.SetWindowTitle("TideFTP"))
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case transfer.Event:
		// Applying an event can finish a transfer, which frees a slot for the
		// next queued one, which is why startQueuedTransfers runs here too.
		wasAtBottom := m.isAtBottomPane()
		m.applyTransferEvent(msg)
		m.startQueuedTransfers()
		m.settleBottomOffset(wasAtBottom)
		return m, waitForTransferEvent(m.engine.Events())
	case transferStreamClosed:
		return m, nil
	case tea.MouseMsg:
		return m.updateMouse(msg)
	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

// updateKey handles a key press. Actions taken here (queuing a transfer,
// logging a connect attempt, a transfer advancing) can grow or shrink the
// row count for whichever bottom-pane tab is active; the deferred settle
// call keeps the view pinned to the bottom if it was already there
// (auto-follow), on every return path, without needing it duplicated at
// each early return. Two escape hatches opt out of the follow snap: tabSwitch
// (setBottomTab already picks the right offset for a freshly opened tab) and
// manualScroll (the user is deliberately scrolling the bottom pane, so their
// input must not be overridden by auto-follow — it's still clamped though).
func (m Model) updateKey(msg tea.KeyMsg) (result tea.Model, cmd tea.Cmd) {
	wasAtBottom := m.isAtBottomPane()
	tabSwitch := false
	manualScroll := false
	defer func() {
		next, ok := result.(Model)
		if !ok || tabSwitch {
			return
		}
		if manualScroll {
			next.clampBottomOffset()
		} else {
			next.settleBottomOffset(wasAtBottom)
		}
		result = next
	}()

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
		return m, tea.Sequence(closeEngine(m.engine), tea.Quit)
	case "tab":
		m.focus = (m.focus + 1) % 3
	case "shift+tab":
		m.focus = (m.focus + 2) % 3
	case "up", "k":
		manualScroll = m.focus == focusQueue
		m.moveCursor(-1)
	case "down", "j":
		manualScroll = m.focus == focusQueue
		m.moveCursor(1)
	case "pgup":
		manualScroll = m.focus == focusQueue
		m.moveCursor(-10)
	case "pgdown":
		manualScroll = m.focus == focusQueue
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
	case "i":
		m.showIcons = !m.showIcons
		if m.showIcons {
			m.setStatus("icons on")
		} else {
			m.setStatus("icons off")
		}
	case "u":
		m.queueUpload()
	case "d":
		m.queueDownload()
	case "o":
		m.overlay = overlayConflict
	case "x":
		m.cancelActiveTransfers()
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
		tabSwitch = true
		m.setBottomTab(tabQueue)
	case "2":
		tabSwitch = true
		m.setBottomTab(tabActive)
	case "3":
		tabSwitch = true
		m.setBottomTab(tabFailed)
	case "4":
		tabSwitch = true
		m.setBottomTab(tabHistory)
	case "5":
		tabSwitch = true
		m.setBottomTab(tabLog)
	}
	m.clampCursors()
	return m, nil
}

func (m Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress && msg.Action != tea.MouseActionMotion {
		return m, nil
	}
	// topPaneHeight is what View actually draws with, so the hit test and
	// the drawn boundary agree even when the layout clamp bites.
	topHeight := m.topPaneHeight()
	bottomStart := 1 + topHeight
	localWidth := max(20, int(float64(m.width)*m.fileSplit.Value()))
	switch {
	case msg.Y < bottomStart && msg.X < localWidth:
		m.focus = focusLocal
		m.cursorFromMouse(&m.local, msg.Y)
	case msg.Y < bottomStart:
		m.focus = focusRemote
		m.cursorFromMouse(&m.remote, msg.Y)
	default:
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
		m.local.entries = nil
		m.local.cursor, m.local.offset = 0, 0
		m.setError(err.Error())
		return
	}
	m.local.entries = entries
	m.local.cursor = min(m.local.cursor, max(0, len(entries)-1))
}

// setLocalPath moves the local pane to dirPath, keeping the pane where it is
// if the new directory cannot be read. Entering a directory always starts a
// fresh selection: selections are keyed by bare name, so carrying them across
// a directory change would silently mark same-named files in the new one.
func (m *Model) setLocalPath(dirPath string) {
	entries, err := listLocal(dirPath, m.local.showHidden)
	if err != nil {
		m.setError(err.Error())
		return
	}
	m.local.path = dirPath
	m.local.entries = entries
	m.local.reset()
}

func (m *Model) setRemotePath(dirPath string) {
	m.remote.path = dirPath
	m.remote.entries = m.remoteFS.List(dirPath, m.remote.showHidden)
	m.remote.reset()
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
		m.bottomOffset = max(0, m.bottomOffset+delta)
	}
}

func (m *Model) activateCursor() {
	switch m.focus {
	case focusLocal:
		entry, ok := m.local.current()
		if !ok || !entry.IsDir() {
			return
		}
		m.setLocalPath(filepath.Join(m.local.path, entry.Name))
	case focusRemote:
		entry, ok := m.remote.current()
		if !ok || !entry.IsDir() {
			return
		}
		m.setRemotePath(m.remoteFS.Child(m.remote.path, entry.Name))
	}
}

func (m *Model) parentDir() {
	switch m.focus {
	case focusLocal:
		parent := filepath.Dir(m.local.path)
		if parent != m.local.path {
			m.setLocalPath(parent)
		}
	case focusRemote:
		m.setRemotePath(m.remoteFS.Parent(m.remote.path))
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
			BytesTotal:  max(total, int64(64_000)),
			Status:      domain.Queued,
			Message:     "queued",
		})
		m.nextTransferID++
	}
	m.setStatus(fmt.Sprintf("queued %d transfer(s)", len(entries)))
	m.startQueuedTransfers()
}

// startQueuedTransfers promotes queued transfers to active, up to maxParallel,
// handing each one to the engine. Nothing else moves a transfer forward: all
// progress after this point arrives as transfer.Event.
func (m *Model) startQueuedTransfers() {
	active := countStatus(m.transfers, domain.Active)
	for i := range m.transfers {
		if active >= m.maxParallel {
			return
		}
		if m.transfers[i].Status != domain.Queued {
			continue
		}
		m.transfers[i].Status = domain.Active
		m.transfers[i].StartedAt = time.Now()
		m.transfers[i].Message = "transferring"
		m.engine.Start(transfer.Request{
			ID:          m.transfers[i].ID,
			Direction:   m.transfers[i].Direction,
			Source:      m.transfers[i].Source,
			Destination: m.transfers[i].Destination,
			Size:        m.transfers[i].BytesTotal,
		})
		active++
	}
}

// applyTransferEvent folds one engine event into the queue row it belongs to.
// Events for unknown IDs are ignored: the engine may still be draining a
// transfer the UI has already forgotten.
func (m *Model) applyTransferEvent(event transfer.Event) {
	index := m.transferIndex(event.ID)
	if index < 0 {
		return
	}
	row := &m.transfers[index]
	row.BytesDone = min(event.BytesDone, row.BytesTotal)
	switch event.Kind {
	case transfer.Progress:
		row.Status = domain.Active
		row.Message = "transferring"
	case transfer.Completed:
		row.BytesDone = row.BytesTotal
		row.Status = domain.Done
		row.FinishedAt = time.Now()
		row.Message = "complete"
	case transfer.Failed:
		row.Status = domain.Failed
		row.FinishedAt = time.Now()
		row.Message = failureMessage(event.Err)
		m.setError(fmt.Sprintf("transfer %d failed: %s", row.ID, row.Message))
	case transfer.Canceled:
		// domain has no Canceled status yet, so a canceled transfer lands in
		// the Failed tab, which is also where the retry flow will live.
		row.Status = domain.Failed
		row.FinishedAt = time.Now()
		row.Message = "canceled"
		m.logs = append(m.logs, fmt.Sprintf("transfer %d canceled", row.ID))
	}
}

// cancelActiveTransfers stops everything in flight. It is all-or-nothing
// because the transfers pane scrolls but has no row cursor to aim at yet.
func (m *Model) cancelActiveTransfers() {
	ids := make([]int, 0, len(m.transfers))
	for _, row := range m.transfers {
		if row.Status == domain.Active {
			ids = append(ids, row.ID)
		}
	}
	if len(ids) == 0 {
		m.setError("no active transfers to cancel")
		return
	}
	for _, id := range ids {
		m.engine.Cancel(id)
	}
	m.setStatus(fmt.Sprintf("cancelling %d transfer(s)", len(ids)))
}

func (m Model) transferIndex(id int) int {
	for i := range m.transfers {
		if m.transfers[i].ID == id {
			return i
		}
	}
	return -1
}

func failureMessage(err error) string {
	if err == nil {
		return "failed"
	}
	return err.Error()
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
	visible := m.filePaneVisibleRows()
	m.local.clamp(visible)
	m.remote.clamp(visible)
}

// setBottomTab switches the focused bottom-pane tab and resets its scroll
// position. The log tab opens scrolled to the latest entries, matching a
// tail view; the transfer tabs open scrolled to the top.
func (m *Model) setBottomTab(tab bottomTab) {
	m.bottomTab = tab
	if tab == tabLog {
		m.bottomOffset = max(0, len(m.logs)-m.bottomVisibleRows())
	} else {
		m.bottomOffset = 0
	}
}

func (m *Model) clampBottomOffset() {
	m.bottomOffset = min(m.bottomOffset, max(0, m.bottomRowCount()-m.bottomVisibleRows()))
	m.bottomOffset = max(0, m.bottomOffset)
}

// isAtBottomPane reports whether the bottom pane is currently scrolled all
// the way down for its tab, i.e. showing the latest rows.
func (m Model) isAtBottomPane() bool {
	return m.bottomOffset >= max(0, m.bottomRowCount()-m.bottomVisibleRows())
}

// settleBottomOffset re-clamps the scroll offset after the row count for the
// current tab may have changed. If the pane was already scrolled to the
// bottom, it stays pinned there (auto-follow) so new transfer activity or
// log lines stay visible; otherwise it's just clamped to the valid range.
func (m *Model) settleBottomOffset(wasAtBottom bool) {
	if wasAtBottom {
		m.bottomOffset = max(0, m.bottomRowCount()-m.bottomVisibleRows())
		return
	}
	m.clampBottomOffset()
}

func (m *Model) cursorFromMouse(pane *filePane, y int) {
	row := y - firstFileRow
	if row < 0 {
		return
	}
	pane.cursor = pane.offset + row
	pane.clamp(m.filePaneVisibleRows())
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

// clamp keeps the cursor inside the entry list and scrolls the offset so the
// cursor stays within the visible window, where visible is how many entry rows
// the pane can actually draw at the current terminal size.
func (p *filePane) clamp(visible int) {
	visible = max(1, visible)
	p.cursor = min(max(0, p.cursor), max(0, len(p.entries)-1))
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+visible {
		p.offset = p.cursor - visible + 1
	}
	p.offset = min(p.offset, max(0, len(p.entries)-visible))
	p.offset = max(0, p.offset)
}

// reset returns a pane to the top of a freshly entered directory and drops
// the previous directory's selection.
func (p *filePane) reset() {
	p.cursor, p.offset = 0, 0
	p.selected = map[string]bool{}
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

// waitForTransferEvent blocks a command goroutine on the engine's event
// channel and delivers the next event as a message. Update re-issues it after
// each event, which is the standard Bubble Tea way to pump an external stream.
func waitForTransferEvent(events <-chan transfer.Event) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return transferStreamClosed{}
		}
		return event
	}
}

func closeEngine(engine transfer.Engine) tea.Cmd {
	return func() tea.Msg {
		_ = engine.Close()
		return nil
	}
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
