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

// statsGradient is a low-to-high intensity ramp used to color the
// throughput graph's columns by how close each one is to the visible
// window's peak — blue at the quiet end, through violet and magenta, up to
// hot pink at the busiest — brighter/hotter means more throughput, not
// just a taller point on the line.
var statsGradient = []lipgloss.Color{
	"#1E2A78",
	"#33267F",
	"#4C2394",
	"#6B21A8",
	"#8B21B3",
	"#B21FAE",
	"#D91FA0",
	"#F23FA6",
	"#FF69B4",
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

// smoothWindow is smoothSamples' trailing-average width: enough to take
// the jitter off a noisy 1-second reading without smearing a genuine spike
// into invisibility.
const smoothWindow = 3

// smoothSamples applies a trailing moving average of smoothWindow samples,
// so the line reads as a flowing curve rather than jittering with every
// raw reading's noise. Returns a slice the same length as samples.
func smoothSamples(samples []int64) []int64 {
	if len(samples) == 0 {
		return samples
	}
	smoothed := make([]int64, len(samples))
	var sum int64
	for i, v := range samples {
		sum += v
		if i >= smoothWindow {
			sum -= samples[i-smoothWindow]
		}
		smoothed[i] = sum / int64(min(i+1, smoothWindow))
	}
	return smoothed
}

// brailleBits maps a sub-pixel's (column, row) position within one braille
// cell — column 0/1 left/right, row 0-3 top-to-bottom — to the bit it
// contributes to that cell's Unicode Braille Pattern codepoint, per the
// standard dot numbering (1,2,3,7 left top-to-bottom, 4,5,6,8 right).
var brailleBits = [2][4]int{
	{0x01, 0x02, 0x04, 0x40},
	{0x08, 0x10, 0x20, 0x80},
}

const brailleBase = 0x2800

// bresenhamRun calls plot(x, y) for every integer point on the line from
// (x0,y0) to (x1,y1) inclusive — the standard integer line algorithm, used
// here so two adjacent sub-columns whose values jump by more than one
// sub-row still connect as one continuous stroke instead of two
// disconnected dots.
func bresenhamRun(x0, y0, x1, y1 int, plot func(x, y int)) {
	dx, sx := abs(x1-x0), 1
	if x0 > x1 {
		sx = -1
	}
	dy, sy := -abs(y1-y0), 1
	if y0 > y1 {
		sy = -1
	}
	err := dx + dy
	x, y := x0, y0
	for {
		plot(x, y)
		if x == x1 && y == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x += sx
		}
		if e2 <= dx {
			err += dx
			y += sy
		}
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// renderThroughputLine draws samples (bytes/sec, oldest first) as a
// connected line using Unicode Braille dots — 2 sub-columns and 4 sub-rows
// per terminal cell, so the plotted resolution (and how much history fits
// across the same width) is double what one sample per terminal column
// would give. Samples are windowed to the last width*2 of them
// (right-aligned, left-padded with zeros if there aren't enough yet),
// lightly smoothed, then connected sub-pixel to sub-pixel with
// bresenhamRun so a steep jump between readings still looks like one
// stroke. Each terminal column is tinted along statsGradient by its own
// value. Returns exactly height ANSI-styled lines, each width printable
// columns wide, on the Stats tab's fixed black background, or nil if
// width or height isn't positive.
func renderThroughputLine(samples []int64, width, height int) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	subWidth, subHeight := width*2, height*4

	window := make([]int64, subWidth)
	start := max(0, len(samples)-subWidth)
	visible := samples[start:]
	copy(window[subWidth-len(visible):], visible)
	smoothed := smoothSamples(window)

	peak := int64(1)
	for _, v := range smoothed {
		if v > peak {
			peak = v
		}
	}

	// y[i] is sub-column i's height from the bottom, in sub-rows.
	y := make([]int, subWidth)
	for i, v := range smoothed {
		level := int(math.Round(float64(v) / float64(peak) * float64(subHeight-1)))
		y[i] = min(max(level, 0), subHeight-1)
	}

	dots := make([][]bool, subWidth)
	for i := range dots {
		dots[i] = make([]bool, subHeight)
	}
	plot := func(x, yFromBottom int) {
		if x < 0 || x >= subWidth {
			return
		}
		dots[x][subHeight-1-min(max(yFromBottom, 0), subHeight-1)] = true
	}
	plot(0, y[0])
	for i := 1; i < subWidth; i++ {
		bresenhamRun(i-1, y[i-1], i, y[i], plot)
	}

	colorFor := func(cellX int) lipgloss.Color {
		level := max(y[cellX*2], y[cellX*2+1])
		frac := float64(level) / float64(subHeight-1)
		idx := int(frac * float64(len(statsGradient)-1))
		return statsGradient[min(max(idx, 0), len(statsGradient)-1)]
	}

	rows := make([]string, height)
	for r := range height {
		var line strings.Builder
		for c := range width {
			bits := 0
			for subCol := range 2 {
				for subRow := range 4 {
					if dots[c*2+subCol][r*4+subRow] {
						bits |= brailleBits[subCol][subRow]
					}
				}
			}
			line.WriteString(segment(statsBackground, colorFor(c), string(rune(brailleBase+bits))))
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
// (statsLine, renderThroughputLine). Below a usable-graph floor it
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
	lines = append(lines, renderThroughputLine(m.statsHistory, width, graphHeight)...)
	lines = append(lines, line2)
	return lines
}
