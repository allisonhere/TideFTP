package ui

import (
	"fmt"
	"math"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/allisonhere/tideui"

	"tideftp/internal/domain"
)

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

	// eighths[col] is how many eighth-rows (0..height*8) that column fills.
	eighths := make([]int, width)
	for i, v := range window {
		e := int(math.Round(float64(v) / float64(peak) * float64(height*8)))
		eighths[i] = min(max(e, 0), height*8)
	}

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

// knownProtocols fixes the display order of the per-protocol breakdown —
// only protocols actually seen this session get a line.
var knownProtocols = []string{"sftp", "ftp", "ftps"}

// renderStatsTab composes the Stats tab's content: a live snapshot line,
// the throughput graph (most of the available height), session totals and
// averages, and one line per protocol actually seen this session.
// Degrades to a handful of text lines with no graph below a usable-graph
// floor, mirroring how renderBottomPane itself falls back to "no rows yet"
// when there's no room for anything at all.
func (m Model) renderStatsTab(renderer tideui.Renderer, width, height int) []string {
	if height <= 0 {
		return nil
	}

	snapshot := fitRow(renderer.Styles.DetailBody, width, fmt.Sprintf(
		"Active %d · Queued %d · ↕ %s", m.stats.activeCount, m.stats.queuedCount, formatRate(m.stats.currentThroughput)))
	totals := fitRow(renderer.Styles.DetailMeta, width, fmt.Sprintf(
		"Transferred %s · Done %d · Failed %d", formatSize(m.stats.bytesTransferred), m.stats.doneCount, m.stats.failedCount))
	averages := fitRow(renderer.Styles.DetailMeta, width, fmt.Sprintf(
		"Avg speed %s · Avg size %s", formatRate(int64(m.stats.avgSpeed)), formatSize(m.stats.avgFileSize)))

	var protocolLines []string
	for _, proto := range knownProtocols {
		ps, ok := m.stats.byProtocol[proto]
		if !ok {
			continue
		}
		protocolLines = append(protocolLines, fitRow(renderer.Styles.DetailMeta, width, fmt.Sprintf(
			"%s: %d done, %d failed, %s", proto, ps.done, ps.failed, formatSize(ps.bytes))))
	}
	tail := append([]string{totals, averages}, protocolLines...)

	// Below a usable floor, drop the graph and show as many of the fixed
	// lines as fit, snapshot first.
	if height <= len(tail)+2 {
		lines := append([]string{snapshot}, tail...)
		return lines[:min(len(lines), height)]
	}

	graphHeight := height - 1 - len(tail)
	lines := make([]string, 0, height)
	lines = append(lines, snapshot)
	for _, line := range renderThroughputGraph(m.statsHistory, width, graphHeight) {
		lines = append(lines, fitRow(renderer.Styles.DetailBody, width, line))
	}
	lines = append(lines, tail...)
	return lines
}
