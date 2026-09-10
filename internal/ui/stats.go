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
	"tideftp/internal/session"
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

// statsGlow is the top of the throughput graph's backdrop, which fades to
// black at the bottom of the plot box. The tint is a fixed vertical
// reference rather than a property of the data: a line rising into the green
// is a line running fast.
//
// It is deliberately very dark. It sits behind the brightest, most
// interesting part of the graph, so this is exactly where the line has the
// least contrast to work with — see
// TestStatsGraphStaysReadableAgainstItsBackdrop, which is what stops anyone
// lightening it until the peaks wash out.
var statsGlow = lipgloss.Color("#0A2A12")

// statsRowBackground is the backdrop for one row of the plot box, row 0 at
// the top. It interpolates statsGlow down to black rather than indexing a
// fixed ramp because the graph's height follows the pane's.
func statsRowBackground(row, height int) lipgloss.Color {
	if height <= 1 || row <= 0 {
		return statsGlow
	}
	r, g, b, ok := hexToRGB(statsGlow)
	if !ok {
		return statsBackground
	}
	// Linear in sRGB: the ramp only has a handful of rows to cover, and a
	// perceptual curve would spend most of them near-black.
	remaining := 1 - float64(min(row, height-1))/float64(height-1)
	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X",
		int(r*255*remaining+0.5),
		int(g*255*remaining+0.5),
		int(b*255*remaining+0.5),
	))
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
	totalBytes               int64   // sum of BytesTotal across every transfer this session — what bytesTransferred is a fraction of
	avgSpeed                 float64 // bytes/sec, mean over completed transfers
	avgFileSize              int64   // mean BytesTotal over completed transfers
	currentThroughput        int64   // bytes/sec, this tick's sample
	byProtocol               map[string]protocolStats
}

// percentDone is bytesTransferred as a percentage of totalBytes, 0 when
// there's nothing to measure yet rather than dividing by zero.
func (s statsSnapshot) percentDone() float64 {
	if s.totalBytes <= 0 {
		return 0
	}
	return float64(s.bytesTransferred) / float64(s.totalBytes) * 100
}

type protocolStats struct {
	done, failed int
	bytes        int64
}

// statsHistoryCap bounds how many throughput samples the graph keeps —
// 5 minutes at statsTickInterval, deliberately more than any realistic
// graph width, so history isn't truncated by the display before it's
// truncated by the cap.
const statsHistoryCap = int(5 * time.Minute / statsTickInterval)

// statsTickInterval is how often a new point is added to the graph. It says
// nothing about how far back each point measures — that is statsRateWindow —
// so it can be short enough to feel live without the reading getting noisy.
const statsTickInterval = 250 * time.Millisecond

// statsByteSample is one reading of the running total of bytes transferred,
// timestamped so a rate can be measured across several of them.
type statsByteSample struct {
	at    time.Time
	bytes int64
}

// statsRateWindow is how far back each throughput reading measures.
//
// It exists because a running transfer only updates its byte count every
// transfer.ProgressInterval. Measuring between two consecutive ticks meant
// the reading depended on how many of those updates happened to land in that
// particular tick, and at any tick interval that is not an exact multiple of
// the reporting interval the two beat against each other: at 250ms against
// 200ms reporting, a perfectly constant transfer read 0.8x, 0.8x, 0.8x, 1.6x,
// forever. The graph then scaled itself to a 1.6x that was pure artifact and
// drew the real rate as a flat band two thirds up the box, with no peaks.
//
// Measuring across a window several reports wide averages that beat out: the
// count of updates inside the window barely changes from tick to tick, so
// what is left is the actual rate.
const statsRateWindow = time.Second

// peakLookbackWindows is how many screenfuls back renderThroughputLine looks
// when deciding the graph's ceiling. One would mean the scale jumped every
// time a high reading scrolled off; the whole history means a single early
// spike flattens the graph for the rest of the connection. Two holds the
// scale steady for as long as a record is visible, then lets it recover.
const peakLookbackWindows = 2

// statsTickMsg drives throughput sampling. It's the only periodic ticker
// anywhere in internal/ui — everything else redraws only in response to a
// key, a transfer event, a listing reply, or a resize.
type statsTickMsg struct{}

func statsTickCmd() tea.Cmd {
	return tea.Tick(statsTickInterval, func(time.Time) tea.Msg { return statsTickMsg{} })
}

// startStatsSampling begins sampling for a new connection, from scratch.
//
// Sampling is tied to the connection rather than to the Stats tab being
// visible: the graph is a record of what this connection did, and a user who
// looks at the queue while a transfer runs and then looks back expects to see
// the part they missed, not an empty graph starting from the moment they
// returned. It stops at disconnect, which is also the only thing that clears
// the history — see stopStatsSampling.
func (m *Model) startStatsSampling() tea.Cmd {
	m.statsHistory = nil
	m.statsBytes = nil
	m.stats = m.computeStats()
	// statsSampling gates the self-perpetuating tick chain, so setting it
	// here and checking it in applyStatsTick is what stops a reconnect from
	// leaving two chains running and sampling everything twice.
	if m.statsSampling {
		return nil
	}
	m.statsSampling = true
	return statsTickCmd()
}

// stopStatsSampling ends sampling and clears the graph. The chain itself
// winds down on the next tick, when applyStatsTick sees the flag is off.
func (m *Model) stopStatsSampling() {
	m.statsSampling = false
	m.statsHistory = nil
	m.statsBytes = nil
	m.stats = m.computeStats()
}

// applyStatsTick recomputes the snapshot and appends one throughput
// sample, then re-arms itself for as long as the connection lasts. It keeps
// sampling with the Stats tab hidden — that is the point, so the graph has
// the history to show when the user looks back — and the chain terminates on
// disconnect, when stopStatsSampling clears the flag.
func (m *Model) applyStatsTick() tea.Cmd {
	if !m.statsSampling {
		return nil
	}
	now := time.Now()
	snapshot := m.computeStats()
	m.statsBytes = append(m.statsBytes, statsByteSample{at: now, bytes: snapshot.bytesTransferred})
	m.trimStatsBytes(now)
	if oldest := m.statsBytes[0]; len(m.statsBytes) > 1 {
		if elapsed := now.Sub(oldest.at).Seconds(); elapsed > 0 {
			rate := int64(float64(snapshot.bytesTransferred-oldest.bytes) / elapsed)
			if rate < 0 {
				// Can happen if a queued Resume transfer (BytesDone already
				// counting its resume offset) was cancelled and removed
				// within the window, momentarily shrinking the total. Never a
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
	m.stats = snapshot
	return statsTickCmd()
}

// trimStatsBytes drops byte readings that have aged out of the rate window,
// keeping the newest one that is already older than it so the measurement
// still spans the full window rather than shrinking to whatever is left
// inside it.
func (m *Model) trimStatsBytes(now time.Time) {
	cut := 0
	for i, sample := range m.statsBytes {
		if now.Sub(sample.at) <= statsRateWindow {
			break
		}
		cut = i
	}
	if cut > 0 {
		m.statsBytes = m.statsBytes[cut:]
	}
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
		snapshot.totalBytes += t.BytesTotal
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
// stroke. The line is drawn in one colour over a backdrop that fades from
// statsGlow at the top of the box to black at the bottom, so height is the
// only thing encoding magnitude and the tint is a fixed reference behind it.
// Returns exactly height ANSI-styled lines, each width printable columns
// wide, or nil if width or height isn't positive.
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

	// peak scales both height and colour, and is measured over more than is
	// on screen: if it were only the visible window, the instant a high
	// value scrolled off the left edge everything still showing would
	// rescale taller and hotter, every tick.
	//
	// It is deliberately not the whole history either. Sampling runs for the
	// life of the connection now, so history outlasts the view by minutes,
	// and a ceiling anchored to all of it lets one early spike flatten every
	// later transfer into the bottom row — the graph stops showing peaks at
	// all. Looking back a couple of windows keeps the scale steady while a
	// record is on screen, and lets it fall back once that record has been
	// gone for as long again.
	// The ceiling is taken from the smoothed series, because that is what
	// gets drawn. Scaling a smoothed curve against a raw maximum it can never
	// reach — smoothing pulls every spike down — just leaves the top of the
	// box permanently empty.
	peakFrom := max(0, len(samples)-subWidth*peakLookbackWindows)
	peak := int64(1)
	for _, v := range smoothSamples(samples[peakFrom:]) {
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

	rows := make([]string, height)
	for r := range height {
		// One backdrop per row, used for the cells and for whatever padding
		// clampView adds — pad with anything else and every row ends in a
		// notch of the wrong colour.
		bg := statsRowBackground(r, height)
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
			line.WriteString(segment(bg, statsForeground, string(rune(brailleBase+bits))))
		}
		rows[r] = clampView(line.String(), width, 1, bg)
	}
	return rows
}

// knownProtocols fixes the display order of the per-protocol breakdown —
// only protocols actually seen this session get a line.
var knownProtocols = []string{
	session.ProtocolSFTP,
	session.ProtocolFTP,
	session.ProtocolFTPS,
	session.ProtocolFTPSImplicit,
}

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
		"Active %d · Queued %d · ↕ %s · %s of %s (%.0f%%)",
		m.stats.activeCount, m.stats.queuedCount, formatRate(m.stats.currentThroughput),
		formatSize(m.stats.bytesTransferred), formatSize(m.stats.totalBytes), m.stats.percentDone()))

	summary := fmt.Sprintf("Done %d · Failed %d · Avg %s (%s files)",
		m.stats.doneCount, m.stats.failedCount,
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
