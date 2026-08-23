package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/allisonhere/tideui"

	"tideftp/internal/domain"
)

// The Stats tab paints its own fixed black-on-green palette rather than
// going through the active theme, on request — a deliberate exception to
// "everything follows the theme," the same way a terminal monitoring
// widget (htop, a VU meter) usually commits to one look regardless of the
// surrounding color scheme.
var (
	statsBackground = lipgloss.Color("#000000")
	statsForeground = lipgloss.Color("#33FF66")
	statsMeta       = lipgloss.Color("#1F9D4A")
)

// statsGradient is a low-to-high intensity ramp, all in the green family,
// used to color the throughput graph's columns by how close each one is to
// the visible window's peak — brighter means more throughput, not just a
// taller bar.
var statsGradient = []lipgloss.Color{
	"#0B3D0B",
	"#146616",
	"#1E8A21",
	"#2BAF2E",
	"#3FD43F",
	"#66FF66",
	"#AFFF8C",
}

// statsLine renders one full-width row of Stats content on the tab's fixed
// black background — the same explicit-background-per-span discipline
// segment/clampView already use for the transfer rows, so a shorter line's
// padding never shows through as the theme's background instead.
func statsLine(width int, fg lipgloss.Color, text string) string {
	return clampView(segment(statsBackground, fg, text), width, 1, statsBackground)
}

// statsSnapshot is the Stats tab's numbers, recomputed from m.transfers —
// the single source of truth for what happened this session — on every
// tick. Only currentThroughput needs a previous sample to diff against
// (filled in by applyStatsTick); everything else is a pure aggregate over
// m.transfers as it stands right now.
type statsSnapshot struct {
	activeCount, queuedCount int
	doneCount, failedCount   int
	bytesTransferred         int64   // sum of BytesDone across every transfer this session
	avgSpeed                 float64 // bytes/sec, mean over completed transfers
	avgFileSize              int64   // mean BytesTotal over completed transfers
	currentThroughput        int64   // bytes/sec, this tick's sample
	byProtocol               map[string]protocolStats
}

type protocolStats struct {
	done, failed int
	bytes        int64
}

// statsHistoryCap bounds how many throughput samples the Stats tab keeps —
// 5 minutes of 1-second samples, deliberately more than any realistic
// graph width, so history isn't truncated by the display before it's
// truncated by the cap.
const statsHistoryCap = 300

const statsTickInterval = time.Second

// statsTickMsg drives the Stats tab's sampling. It's the only periodic
// ticker anywhere in internal/ui — everything else redraws only in
// response to a key, a transfer event, a listing reply, or a resize.
type statsTickMsg struct{}

func statsTickCmd() tea.Cmd {
	return tea.Tick(statsTickInterval, func(time.Time) tea.Msg { return statsTickMsg{} })
}

// resetStatsSampling (re)starts the Stats tab's sampling from scratch —
// called whenever the tab is opened, including switching back to it after
// looking away. This is why the graph shows a gap rather than continuous
// history across tab switches: sampling only runs while the tab is open,
// so there is nothing to resume from.
func (m *Model) resetStatsSampling() tea.Cmd {
	m.statsHistory = nil
	m.statsLastBytes = 0
	m.statsLastSampleAt = time.Time{}
	m.stats = m.computeStats()
	return statsTickCmd()
}

// applyStatsTick recomputes the snapshot and appends one throughput
// sample, then re-arms itself only if the Stats tab is still open — the
// self-terminating chain that makes the ticker cost nothing once the user
// looks away.
func (m *Model) applyStatsTick() tea.Cmd {
	if m.bottomTab != tabStats {
		return nil
	}
	now := time.Now()
	snapshot := m.computeStats()
	if !m.statsLastSampleAt.IsZero() {
		if elapsed := now.Sub(m.statsLastSampleAt).Seconds(); elapsed > 0 {
			rate := int64(float64(snapshot.bytesTransferred-m.statsLastBytes) / elapsed)
			if rate < 0 {
				// Can happen if a queued Resume transfer (BytesDone already
				// counting its resume offset) was cancelled and removed
				// between ticks, momentarily shrinking the total. Never a
				// real negative rate.
				rate = 0
			}
			snapshot.currentThroughput = rate
			m.statsHistory = append(m.statsHistory, rate)
			if len(m.statsHistory) > statsHistoryCap {
				m.statsHistory = m.statsHistory[len(m.statsHistory)-statsHistoryCap:]
			}
		}
	}
	m.statsLastBytes, m.statsLastSampleAt = snapshot.bytesTransferred, now
	m.stats = snapshot
	return statsTickCmd()
}

// computeStats aggregates m.transfers into a fresh statsSnapshot.
// currentThroughput is left zero here — only applyStatsTick can fill it
// in, since it needs a previous sample to diff against.
func (m Model) computeStats() statsSnapshot {
	snapshot := statsSnapshot{byProtocol: map[string]protocolStats{}}
	var totalSpeed float64
	var totalFileSize int64
	for _, t := range m.transfers {
		snapshot.bytesTransferred += t.BytesDone
		switch t.Status {
		case domain.Queued:
			snapshot.queuedCount++
		case domain.Active:
			snapshot.activeCount++
		case domain.Done:
			snapshot.doneCount++
			totalFileSize += t.BytesTotal
			if d := t.FinishedAt.Sub(t.StartedAt).Seconds(); d > 0 {
				totalSpeed += float64(t.BytesTotal) / d
			}
		case domain.Failed, domain.Canceled:
			snapshot.failedCount++
		}
		if t.Protocol == "" {
			continue
		}
		ps := snapshot.byProtocol[t.Protocol]
		ps.bytes += t.BytesDone
		switch t.Status {
		case domain.Done:
			ps.done++
		case domain.Failed, domain.Canceled:
			ps.failed++
		}
		snapshot.byProtocol[t.Protocol] = ps
	}
	if snapshot.doneCount > 0 {
		snapshot.avgSpeed = totalSpeed / float64(snapshot.doneCount)
		snapshot.avgFileSize = totalFileSize / int64(snapshot.doneCount)
	}
	return snapshot
}

// formatRate renders a bytes/sec value the same way formatSize renders a
// byte count, with a trailing "/s".
func formatRate(bytesPerSecond int64) string {
	return formatSize(max(0, bytesPerSecond)) + "/s"
}

// eighthBlocks are the "lower N eighths" Unicode block glyphs, bottom-
// aligned — index 0 is blank, index 8 is a full block. Exactly the
// convention a bar chart growing from the bottom needs.
var eighthBlocks = [9]rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// graphEighths windows samples to the last width of them (right-aligned,
// left-padded with zeros if there aren't enough yet) and reports, for each
// column, how many eighth-rows (0..height*8) it fills relative to the
// window's own peak. Shared by renderThroughputGraph and its colored
// counterpart so the two can never disagree about the underlying shape.
func graphEighths(samples []int64, width, height int) []int {
	window := make([]int64, width)
	start := max(0, len(samples)-width)
	visible := samples[start:]
	copy(window[width-len(visible):], visible)

	peak := int64(1)
	for _, v := range window {
		if v > peak {
			peak = v
		}
	}

	eighths := make([]int, width)
	for i, v := range window {
		e := int(math.Round(float64(v) / float64(peak) * float64(height*8)))
		eighths[i] = min(max(e, 0), height*8)
	}
	return eighths
}

// renderThroughputGraph draws samples (bytes/sec, oldest first) as a
// multi-row block bar chart, most recent sample on the right — the
// reading direction htop's meters use. Returns exactly height lines, each
// width runes wide, or nil if width or height isn't positive. Degrades to
// flat empty bars when every visible sample is zero, rather than dividing
// by zero.
func renderThroughputGraph(samples []int64, width, height int) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	eighths := graphEighths(samples, width, height)

	rows := make([]string, height)
	for r := range height {
		// Row 0 is the top row; a column's band for row r spans eighths
		// [(height-1-r)*8, (height-r)*8).
		floor := (height - 1 - r) * 8
		line := make([]rune, width)
		for c, e := range eighths {
			line[c] = eighthBlocks[min(max(e-floor, 0), 8)]
		}
		rows[r] = string(line)
	}
	return rows
}

// renderThroughputGraphColored is renderThroughputGraph with each column
// tinted along statsGradient by its own fill level — the same eighths that
// decide the glyph also decide the color, so a tall bar is both bigger and
// brighter. Returns ANSI-styled rows, each exactly width printable columns
// wide, on the Stats tab's fixed black background.
func renderThroughputGraphColored(samples []int64, width, height int) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	eighths := graphEighths(samples, width, height)
	maxEighths := height * 8

	colorFor := func(e int) lipgloss.Color {
		frac := float64(e) / float64(maxEighths)
		idx := int(frac * float64(len(statsGradient)-1))
		return statsGradient[min(max(idx, 0), len(statsGradient)-1)]
	}

	rows := make([]string, height)
	for r := range height {
		floor := (height - 1 - r) * 8
		var line strings.Builder
		for _, e := range eighths {
			filled := min(max(e-floor, 0), 8)
			line.WriteString(segment(statsBackground, colorFor(e), string(eighthBlocks[filled])))
		}
		rows[r] = clampView(line.String(), width, 1, statsBackground)
	}
	return rows
}

// knownProtocols fixes the display order of the per-protocol breakdown —
// only protocols actually seen this session get a line.
var knownProtocols = []string{"sftp", "ftp", "ftps"}

// renderStatsTab composes the Stats tab's content: a live snapshot line,
// the throughput graph — sandwiched between the two text lines so it gets
// as much of the available height as possible — and a second line packing
// in session totals, averages, and the per-protocol breakdown. Everything
// here paints the fixed black/green palette rather than the active theme
// (statsLine, renderThroughputGraphColored). Below a usable-graph floor it
// drops to just the two text lines, mirroring how renderBottomPane itself
// falls back to "no rows yet" when there's no room for anything at all.
func (m Model) renderStatsTab(renderer tideui.Renderer, width, height int) []string {
	if height <= 0 {
		return nil
	}

	line1 := statsLine(width, statsForeground, fmt.Sprintf(
		"Active %d · Queued %d · ↕ %s", m.stats.activeCount, m.stats.queuedCount, formatRate(m.stats.currentThroughput)))

	summary := fmt.Sprintf("Done %d · Failed %d · Moved %s · Avg %s (%s files)",
		m.stats.doneCount, m.stats.failedCount, formatSize(m.stats.bytesTransferred),
		formatRate(int64(m.stats.avgSpeed)), formatSize(m.stats.avgFileSize))
	for _, proto := range knownProtocols {
		ps, ok := m.stats.byProtocol[proto]
		if !ok {
			continue
		}
		summary += fmt.Sprintf(" · %s %d/%d %s", proto, ps.done, ps.failed, formatSize(ps.bytes))
	}
	line2 := statsLine(width, statsMeta, summary)

	if height == 1 {
		return []string{line1}
	}
	if height == 2 {
		return []string{line1, line2}
	}

	graphHeight := height - 2
	lines := make([]string, 0, height)
	lines = append(lines, line1)
	lines = append(lines, renderThroughputGraphColored(m.statsHistory, width, graphHeight)...)
	lines = append(lines, line2)
	return lines
}
