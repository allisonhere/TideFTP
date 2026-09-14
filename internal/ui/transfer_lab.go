package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
)

type labScenario struct {
	title string
	hint  string
	files int
	size  int64
	drop  bool
}

var transferLabScenarios = []labScenario{
	{title: "Tiny-file storm", hint: "120 × 4 KB uploads · queue and redraw pressure", files: 120, size: 4 << 10},
	{title: "Large-file batch", hint: "4 × 2 GB uploads · aggregate progress", files: 4, size: 2 << 30},
	{title: "Mixed batch", hint: "24 files from 8 KB to 512 MB", files: 24, size: 0},
	{title: "Drop mid-transfer", hint: "8 × 64 MB · disconnect after 900 ms", files: 8, size: 64 << 20, drop: true},
}

type labDropMsg struct{}

func (m *Model) openTransferLab() {
	if !m.transferLab || !m.connected() {
		m.setError("Transfer Lab requires --transfer-lab and the demo connection")
		return
	}
	m.labCursor = min(max(0, m.labCursor), len(transferLabScenarios)-1)
	m.overlay = overlayTransferLab
	m.setStatus("transfer lab")
}

func (m *Model) handleTransferLabKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q":
		m.overlay = overlayNone
		m.setStatus("transfer lab closed")
	case "up", "k":
		m.labCursor = (m.labCursor - 1 + len(transferLabScenarios)) % len(transferLabScenarios)
	case "down", "j":
		m.labCursor = (m.labCursor + 1) % len(transferLabScenarios)
	case "enter":
		m.overlay = overlayNone
		return m.startTransferLabScenario(transferLabScenarios[m.labCursor])
	}
	return nil
}

func (m *Model) startTransferLabScenario(scenario labScenario) tea.Cmd {
	if !m.transferLab || !m.connected() {
		m.setError("Transfer Lab requires the demo connection")
		return nil
	}
	if !m.queueBusy() {
		m.queueBatchStartID = m.nextTransferID
		m.queueBatchActive = true
	}
	for i := 0; i < scenario.files; i++ {
		size := scenario.size
		if size == 0 {
			// Cycles through a deliberately uneven distribution: lots of small
			// work plus a few files large enough to make total progress useful.
			size = []int64{8 << 10, 64 << 10, 1 << 20, 16 << 20, 128 << 20, 512 << 20}[i%6]
		}
		name := fmt.Sprintf("lab-%03d.bin", i+1)
		m.transfers = append(m.transfers, domain.Transfer{
			ID: m.nextTransferID, Direction: domain.Upload,
			Source:      "/transfer-lab/" + scenario.title + "/" + name,
			Destination: "/incoming/transfer-lab/" + name,
			BytesTotal:  size, Status: domain.Queued, Message: "lab queued", Protocol: m.target.Protocol,
		})
		m.nextTransferID++
	}
	m.setStatus(fmt.Sprintf("transfer lab: queued %s", scenario.title))
	m.appendLog(fmt.Sprintf("transfer lab: %s (%d files)", scenario.title, scenario.files))
	m.startQueuedTransfers()
	if scenario.drop {
		if _, ok := m.conn.(interface{ Drop(error) }); !ok {
			m.setError("Transfer Lab drop scenario requires the demo adapter")
			return nil
		}
		return tea.Tick(900*time.Millisecond, func(time.Time) tea.Msg { return labDropMsg{} })
	}
	return nil
}

func (m Model) renderTransferLab(renderer tideui.Renderer) *tideui.Overlay {
	width := min(72, max(46, m.width-8))
	contentWidth := width - 4
	rows := []string{renderer.Styles.DetailMeta.Width(contentWidth).Render("Developer-only simulated transfers — no real files or server."), ""}
	for i, scenario := range transferLabScenarios {
		rows = append(rows, renderer.RenderSoftRow(tideui.SoftRow{Text: scenario.title, Suffix: scenario.hint, Selected: i == m.labCursor}, contentWidth))
	}
	rows = append(rows, "", renderer.RenderSoftHints(contentWidth,
		tideui.SoftHint{Key: "up/down", Label: "select"},
		tideui.SoftHint{Key: "enter", Label: "run"},
		tideui.SoftHint{Key: "esc", Label: "close"}))
	overlay := renderer.SoftPanelOverlay(tideui.SoftPanel{Prefix: "tideftp", Title: "transfer lab", Width: width, Content: renderer.RenderSoftBody(width, strings.Join(rows, "\n"))})
	return &overlay
}
