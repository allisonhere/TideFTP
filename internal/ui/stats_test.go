package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"tideftp/internal/config"
	"tideftp/internal/domain"
	"tideftp/internal/localfs"
)

func statsTestModel(t *testing.T) Model {
	t.Helper()
	return NewModel(localfs.New(), &stubDialer{}, nil, config.Default(), nil, nil)
}

func TestComputeStatsAggregatesAcrossStatusesAndProtocols(t *testing.T) {
	model := statsTestModel(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	model.transfers = []domain.Transfer{
		{ID: 1, Status: domain.Queued, BytesTotal: 100, Protocol: "sftp"},
		{ID: 2, Status: domain.Active, BytesDone: 40, BytesTotal: 100, Protocol: "sftp"},
		{ID: 3, Status: domain.Done, BytesDone: 200, BytesTotal: 200, StartedAt: start, FinishedAt: start.Add(2 * time.Second), Protocol: "sftp"},
		{ID: 4, Status: domain.Done, BytesDone: 400, BytesTotal: 400, StartedAt: start, FinishedAt: start.Add(4 * time.Second), Protocol: "ftp"},
		{ID: 5, Status: domain.Failed, BytesDone: 10, BytesTotal: 100, Protocol: "ftp"},
		{ID: 6, Status: domain.Canceled, BytesDone: 5, BytesTotal: 100, Protocol: "ftp"},
	}

	snap := model.computeStats()

	if snap.queuedCount != 1 || snap.activeCount != 1 || snap.doneCount != 2 || snap.failedCount != 2 {
		t.Fatalf("counts = %+v, want queued=1 active=1 done=2 failed=2", snap)
	}
	if want := int64(0 + 40 + 200 + 400 + 10 + 5); snap.bytesTransferred != want { // transfer 1 (Queued) has BytesDone 0
		t.Fatalf("bytesTransferred = %d, want %d", snap.bytesTransferred, want)
	}
	// avg speed: transfer 3 = 200B/2s = 100 B/s, transfer 4 = 400B/4s = 100 B/s -> mean 100.
	if snap.avgSpeed != 100 {
		t.Fatalf("avgSpeed = %v, want 100", snap.avgSpeed)
	}
	if snap.avgFileSize != 300 { // mean(200, 400)
		t.Fatalf("avgFileSize = %d, want 300", snap.avgFileSize)
	}
	sftp, ok := snap.byProtocol["sftp"]
	if !ok || sftp.done != 1 || sftp.failed != 0 || sftp.bytes != 0+40+200 {
		t.Fatalf("byProtocol[sftp] = %+v", sftp)
	}
	ftp, ok := snap.byProtocol["ftp"]
	if !ok || ftp.done != 1 || ftp.failed != 2 || ftp.bytes != 400+10+5 {
		t.Fatalf("byProtocol[ftp] = %+v", ftp)
	}
	if _, ok := snap.byProtocol["ftps"]; ok {
		t.Fatalf("byProtocol should not contain a protocol that was never seen")
	}
}

func TestComputeStatsWithNoCompletedTransfersHasZeroAverages(t *testing.T) {
	model := statsTestModel(t)
	model.transfers = []domain.Transfer{{ID: 1, Status: domain.Queued, BytesTotal: 100}}

	snap := model.computeStats()

	if snap.avgSpeed != 0 || snap.avgFileSize != 0 {
		t.Fatalf("averages = %+v, want zero with nothing completed", snap)
	}
}

func TestApplyStatsTickIsANoOpOffTheStatsTab(t *testing.T) {
	model := statsTestModel(t)
	model.bottomTab = tabQueue
	model.transfers = []domain.Transfer{{ID: 1, Status: domain.Done, BytesDone: 100, BytesTotal: 100}}

	cmd := model.applyStatsTick()

	if cmd != nil {
		t.Fatalf("applyStatsTick off the Stats tab returned a cmd, want nil (self-terminating)")
	}
	if len(model.statsHistory) != 0 {
		t.Fatalf("statsHistory = %v, want untouched while off the Stats tab", model.statsHistory)
	}
}

func TestApplyStatsTickSamplesThroughputBetweenTicks(t *testing.T) {
	model := statsTestModel(t)
	model.bottomTab = tabStats
	model.statsLastSampleAt = time.Now().Add(-time.Second)
	model.statsLastBytes = 1000
	model.transfers = []domain.Transfer{{ID: 1, Status: domain.Active, BytesDone: 3000, BytesTotal: 10000}}

	cmd := model.applyStatsTick()

	if cmd == nil {
		t.Fatalf("applyStatsTick on the Stats tab returned nil, want a re-armed tick cmd")
	}
	if len(model.statsHistory) != 1 {
		t.Fatalf("statsHistory = %v, want one sample appended", model.statsHistory)
	}
	// ~2000 bytes over ~1 second; allow slack for the real elapsed time.
	if model.statsHistory[0] < 1500 || model.statsHistory[0] > 2500 {
		t.Fatalf("sampled rate = %d, want roughly 2000 B/s", model.statsHistory[0])
	}
	if model.stats.currentThroughput != model.statsHistory[0] {
		t.Fatalf("stats.currentThroughput = %d, want it to match the appended sample %d", model.stats.currentThroughput, model.statsHistory[0])
	}
}

func TestSetBottomTabResetsStatsSamplingOnEveryEntry(t *testing.T) {
	model := statsTestModel(t)
	model.statsHistory = []int64{111, 222}
	model.statsLastBytes = 999
	model.statsLastSampleAt = time.Now()

	cmd := model.setBottomTab(tabStats)

	if cmd == nil {
		t.Fatalf("setBottomTab(tabStats) returned nil, want the tick-starting cmd")
	}
	if len(model.statsHistory) != 0 {
		t.Fatalf("statsHistory = %v, want reset to empty on entering the tab", model.statsHistory)
	}
	if model.statsLastBytes != 0 || !model.statsLastSampleAt.IsZero() {
		t.Fatalf("stats sample anchors were not reset: bytes=%d at=%v", model.statsLastBytes, model.statsLastSampleAt)
	}
}

func TestSetBottomTabReturnsNilForNonStatsTabs(t *testing.T) {
	model := statsTestModel(t)
	if cmd := model.setBottomTab(tabQueue); cmd != nil {
		t.Fatalf("setBottomTab(tabQueue) returned a cmd, want nil")
	}
}

func TestSmoothSamplesIsATrailingMovingAverage(t *testing.T) {
	// window is 3: smoothed[i] = mean of samples[max(0,i-2)..i].
	got := smoothSamples([]int64{0, 3, 6, 9, 12})
	want := []int64{0, 1, 3, 6, 9} // means: 0, (0+3)/2=1, (0+3+6)/3=3, (3+6+9)/3=6, (6+9+12)/3=9
	if len(got) != len(want) {
		t.Fatalf("smoothSamples = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("smoothSamples = %v, want %v", got, want)
		}
	}
}

func TestSmoothSamplesEmptyInputStaysEmpty(t *testing.T) {
	if got := smoothSamples(nil); len(got) != 0 {
		t.Fatalf("smoothSamples(nil) = %v, want empty", got)
	}
}

func TestBresenhamRunConnectsADiagonal(t *testing.T) {
	var got [][2]int
	bresenhamRun(0, 0, 3, 3, func(x, y int) { got = append(got, [2]int{x, y}) })
	want := [][2]int{{0, 0}, {1, 1}, {2, 2}, {3, 3}}
	if len(got) != len(want) {
		t.Fatalf("bresenhamRun points = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bresenhamRun points = %v, want %v", got, want)
		}
	}
}

func TestBresenhamRunConnectsAVerticalJump(t *testing.T) {
	var got []int
	bresenhamRun(5, 0, 5, 4, func(x, y int) { got = append(got, y) })
	want := []int{0, 1, 2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("bresenhamRun ys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bresenhamRun ys = %v, want %v", got, want)
		}
	}
}

func TestRenderThroughputLineInvalidDimensionsReturnNil(t *testing.T) {
	if rows := renderThroughputLine([]int64{1, 2, 3}, 0, 5); rows != nil {
		t.Fatalf("width 0 = %v, want nil", rows)
	}
	if rows := renderThroughputLine([]int64{1, 2, 3}, 5, 0); rows != nil {
		t.Fatalf("height 0 = %v, want nil", rows)
	}
}

func TestRenderThroughputLineReturnsExactDimensions(t *testing.T) {
	rows := renderThroughputLine([]int64{0, 100, 500, 200, 900, 300}, 6, 3)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	for _, row := range rows {
		if got := lipgloss.Width(row); got != 6 {
			t.Fatalf("row width = %d, want 6: %q", got, row)
		}
	}
}

func TestRenderThroughputLineFlatZeroDrawsABaseline(t *testing.T) {
	// A flat zero history is a flat line along the bottom, not a blank
	// pane — an ECG-style baseline rather than "nothing happened yet"
	// looking identical to "nothing has ever moved through this app".
	rows := renderThroughputLine([]int64{0, 0, 0, 0}, 4, 2)
	if lipgloss.Width(rows[0]) == 0 {
		t.Fatalf("top row missing entirely")
	}
	// Bottom row's braille cells must have at least the bottom sub-row lit
	// (codepoint > the blank braille cell, 0x2800) for every column.
	for _, r := range []rune(ansi.Strip(rows[1])) {
		if r <= 0x2800 {
			t.Fatalf("bottom row has an empty cell %q, want a baseline dot in every column", r)
		}
	}
}

// TestRenderThroughputLineProducesRealANSIColor forces a color profile (go
// test's stdout isn't a terminal, so lipgloss otherwise auto-detects "no
// color" and segment would silently render plain text) to prove
// renderThroughputLine actually emits color codes, not just glyphs.
func TestRenderThroughputLineProducesRealANSIColor(t *testing.T) {
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(previous)

	rows := renderThroughputLine([]int64{0, 500, 2000, 8000}, 4, 2)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "\x1b[") {
		t.Fatalf("rows = %q, want ANSI escape codes present", rows)
	}
}

func TestFormatRateClampsNegativeToZero(t *testing.T) {
	if got := formatRate(-500); got != "0 B/s" {
		t.Fatalf("formatRate(-500) = %q, want %q", got, "0 B/s")
	}
}
