package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
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

func TestRenderThroughputGraphAllZeroSamplesRendersBlank(t *testing.T) {
	rows := renderThroughputGraph([]int64{0, 0, 0}, 3, 2)
	want := []string{"   ", "   "}
	if len(rows) != len(want) || rows[0] != want[0] || rows[1] != want[1] {
		t.Fatalf("rows = %q, want %q", rows, want)
	}
}

func TestRenderThroughputGraphPadsWhenFewerSamplesThanWidth(t *testing.T) {
	// One sample at full scale, width 4: three blank columns on the left,
	// the sample right-aligned in the last column.
	rows := renderThroughputGraph([]int64{100}, 4, 1)
	want := []string{"   █"}
	if len(rows) != 1 || rows[0] != want[0] {
		t.Fatalf("rows = %q, want %q", rows, want)
	}
}

func TestRenderThroughputGraphUsesOnlyTheMostRecentSamples(t *testing.T) {
	// Five samples, width 2: only the last two (40, 50) should be visible;
	// the peak among them is 50, so the last column (50) is full and the
	// second-to-last (40) is 40/50 of a single row -> 8*40/50 = 6.4 -> round 6 eighths.
	rows := renderThroughputGraph([]int64{10, 20, 30, 40, 50}, 2, 1)
	want := []string{"▆█"} // ▆█: 6/8 and 8/8
	if len(rows) != 1 || rows[0] != want[0] {
		t.Fatalf("rows = %q, want %q", rows, want)
	}
}

func TestRenderThroughputGraphInvalidDimensionsReturnNil(t *testing.T) {
	if rows := renderThroughputGraph([]int64{1, 2, 3}, 0, 5); rows != nil {
		t.Fatalf("width 0 = %v, want nil", rows)
	}
	if rows := renderThroughputGraph([]int64{1, 2, 3}, 5, 0); rows != nil {
		t.Fatalf("height 0 = %v, want nil", rows)
	}
}

// TestRenderThroughputGraphColoredProducesRealANSIColor forces a color
// profile (go test's stdout isn't a terminal, so lipgloss otherwise
// auto-detects "no color" and segment would silently render plain text)
// to prove renderThroughputGraphColored actually emits color codes, not
// just the right glyphs.
func TestRenderThroughputGraphColoredProducesRealANSIColor(t *testing.T) {
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(previous)

	rows := renderThroughputGraphColored([]int64{0, 500, 2000, 8000}, 4, 2)
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
