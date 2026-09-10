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
	"tideftp/internal/fakefs"
	"tideftp/internal/localfs"
	"tideftp/internal/session"
	"tideftp/internal/transfer"
)

func statsTestModel(t *testing.T) Model {
	t.Helper()
	return NewModel(localfs.New(), &stubDialer{}, nil, config.Default(), nil, nil, "")
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

// The chain terminates on disconnect, not on looking away from the tab.
func TestApplyStatsTickIsANoOpOnceSamplingStops(t *testing.T) {
	model := statsTestModel(t)
	model.statsSampling = false
	model.transfers = []domain.Transfer{{ID: 1, Status: domain.Done, BytesDone: 100, BytesTotal: 100}}

	cmd := model.applyStatsTick()

	if cmd != nil {
		t.Fatalf("applyStatsTick with sampling stopped returned a cmd, want nil (self-terminating)")
	}
	if len(model.statsHistory) != 0 {
		t.Fatalf("statsHistory = %v, want untouched once sampling has stopped", model.statsHistory)
	}
}

// Sampling with the Stats tab hidden is the whole point: the graph has to
// have the history to show when the user looks back at it.
func TestApplyStatsTickSamplesWhileTheStatsTabIsHidden(t *testing.T) {
	model := statsTestModel(t)
	model.statsSampling = true
	model.bottomTab = tabQueue // looking at something else
	model.statsBytes = []statsByteSample{{at: time.Now().Add(-time.Second), bytes: 1000}}
	model.transfers = []domain.Transfer{{ID: 1, Status: domain.Active, BytesDone: 3000, BytesTotal: 10000}}

	cmd := model.applyStatsTick()

	if cmd == nil {
		t.Fatalf("applyStatsTick off the Stats tab returned nil, want a re-armed tick cmd")
	}
	if len(model.statsHistory) != 1 {
		t.Fatalf("statsHistory = %v, want a sample taken even off the tab", model.statsHistory)
	}
}

func TestApplyStatsTickSamplesThroughputBetweenTicks(t *testing.T) {
	model := statsTestModel(t)
	model.statsSampling = true
	model.bottomTab = tabStats
	model.statsBytes = []statsByteSample{{at: time.Now().Add(-time.Second), bytes: 1000}}
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

// Looking away and back used to wipe the graph. It must not: the history
// belongs to the connection, not to the tab being on screen.
func TestSwitchingBottomTabsKeepsTheGraph(t *testing.T) {
	model := statsTestModel(t)
	model.statsSampling = true
	model.statsHistory = []int64{111, 222}
	anchor := time.Now()
	model.statsBytes = []statsByteSample{{at: anchor, bytes: 999}}

	model.setBottomTab(tabStats)
	model.setBottomTab(tabQueue)
	model.setBottomTab(tabStats)

	if len(model.statsHistory) != 2 {
		t.Fatalf("statsHistory = %v, want the two samples kept across tab switches", model.statsHistory)
	}
	if len(model.statsBytes) != 1 || model.statsBytes[0].bytes != 999 || !model.statsBytes[0].at.Equal(anchor) {
		t.Fatalf("tab switching disturbed the byte readings: %+v", model.statsBytes)
	}
	if !model.statsSampling {
		t.Fatal("tab switching stopped sampling")
	}
}

// Disconnecting is what ends a graph, and what clears it.
func TestDisconnectStopsSamplingAndClearsTheGraph(t *testing.T) {
	model := statsTestModel(t)
	model.statsSampling = true
	model.statsHistory = []int64{111, 222}
	model.statsBytes = []statsByteSample{{at: time.Now(), bytes: 999}}

	model.clearConnection("dropped")

	if model.statsSampling {
		t.Fatal("sampling still running after a disconnect")
	}
	if len(model.statsHistory) != 0 {
		t.Fatalf("statsHistory = %v, want cleared on disconnect", model.statsHistory)
	}
	if len(model.statsBytes) != 0 {
		t.Fatalf("byte readings survived a disconnect: %+v", model.statsBytes)
	}
	if model.applyStatsTick() != nil {
		t.Fatal("the tick chain re-armed itself after a disconnect")
	}
}

// The end-to-end wiring: connecting starts sampling, without the user ever
// opening the Stats tab.
//
// This drives the real connect path rather than using loadedModel, which
// wires a connection in directly and never runs applyConnected — see
// connectModel's comment.
func TestConnectingStartsStatsSampling(t *testing.T) {
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, []session.Target{testTarget}, config.Default(), nil, nil, "")
	model.width, model.height = 120, 36

	if model.statsSampling {
		t.Fatal("sampling started before anything connected")
	}

	model = settle(t, model, model.Init())

	if !model.connected() {
		t.Fatalf("test model did not connect: state=%v", model.state)
	}
	if !model.statsSampling {
		t.Fatal("a live connection is not sampling throughput")
	}
	if model.bottomTab == tabStats {
		t.Fatal("this test is meaningless if the Stats tab is the default")
	}
}

// A reconnect must not leave two chains running and sample everything twice.
func TestStartStatsSamplingDoesNotStackTickChains(t *testing.T) {
	model := statsTestModel(t)

	if cmd := model.startStatsSampling(); cmd == nil {
		t.Fatal("the first start returned no tick cmd")
	}
	if cmd := model.startStatsSampling(); cmd != nil {
		t.Fatal("a second start armed another tick chain, which would double-sample")
	}
	if !model.statsSampling {
		t.Fatal("sampling flag was cleared by the second start")
	}
}

// The rate window has to span several progress reports. If it spans only
// one or two, how many happened to land inside it changes from tick to tick
// and that beat is what gets drawn instead of the transfer rate.
func TestStatsRateWindowSpansSeveralProgressReports(t *testing.T) {
	const wantReports = 4
	if statsRateWindow < wantReports*transfer.ProgressInterval {
		t.Fatalf("statsRateWindow %v spans fewer than %d progress reports of %v",
			statsRateWindow, wantReports, transfer.ProgressInterval)
	}
}

// simulateReportedBytes plays back the byte counter of a transfer running at
// rateFor(tick), advancing only every transfer.ProgressInterval the way a
// real one does, and returns the throughput series applyStatsTick's own
// windowed maths produces from it.
//
// The reporting granularity is the whole point: a reading taken between two
// consecutive ticks depends on how many reports landed in that tick, and at
// 250ms sampling against 200ms reporting a perfectly constant transfer used
// to read 0.8x, 0.8x, 0.8x, 1.6x forever.
func simulateReportedBytes(t *testing.T, rateFor func(tick int) int64, ticks int) []int64 {
	t.Helper()
	model := statsTestModel(t)
	model.statsSampling = true
	model.transfers = []domain.Transfer{{ID: 1, Status: domain.Active, BytesTotal: 1 << 62}}

	base := time.Now()
	var reported int64
	nextReport := transfer.ProgressInterval

	for i := 1; i <= ticks; i++ {
		now := time.Duration(i) * statsTickInterval
		for nextReport <= now {
			reported += int64(float64(rateFor(int(nextReport/statsTickInterval))) * transfer.ProgressInterval.Seconds())
			nextReport += transfer.ProgressInterval
		}
		// applyStatsTick's own window arithmetic, against simulated clock
		// readings rather than time.Now().
		at := base.Add(now)
		model.statsBytes = append(model.statsBytes, statsByteSample{at: at, bytes: reported})
		cut := 0
		for j, sample := range model.statsBytes {
			if at.Sub(sample.at) <= statsRateWindow {
				break
			}
			cut = j
		}
		model.statsBytes = model.statsBytes[cut:]
		if oldest := model.statsBytes[0]; len(model.statsBytes) > 1 {
			if elapsed := at.Sub(oldest.at).Seconds(); elapsed > 0 {
				model.statsHistory = append(model.statsHistory, int64(float64(reported-oldest.bytes)/elapsed))
			}
		}
	}
	return model.statsHistory
}

// A transfer running at a dead-constant rate must read as a constant rate.
func TestThroughputOfAConstantTransferIsSteady(t *testing.T) {
	const rate = 1_000_000
	samples := simulateReportedBytes(t, func(int) int64 { return rate }, 240)

	var lo, hi int64 = 1 << 62, 0
	for _, v := range samples[8:] { // past the initial window fill
		lo, hi = min(lo, v), max(hi, v)
	}
	if spread := float64(hi-lo) / float64(hi); spread > 0.25 {
		t.Fatalf("a constant %d B/s transfer read between %d and %d (%.0f%% spread) — the sampler is beating against the reporting interval", rate, lo, hi, spread*100)
	}
}

// The symptom that prompted all of this: with a real reporting pattern
// underneath, bursty traffic has to actually draw peaks.
func TestGraphDrawsPeaksForBurstyTraffic(t *testing.T) {
	const width, height = 60, 8
	samples := simulateReportedBytes(t, func(tick int) int64 {
		if (tick/20)%2 == 0 {
			return 200_000
		}
		return 1_000_000
	}, 240)

	ink := rowsWithInk(renderThroughputLine(samples, width, height))

	if len(ink) < height-2 {
		t.Fatalf("bursty traffic drew on rows %v of 0..%d — the graph is flattened, not showing peaks", ink, height-1)
	}
	if ink[0] != 0 {
		t.Fatalf("nothing reaches the top row: ink starts at row %d", ink[0])
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

// TestRenderThroughputLinePeakIsScopedToTheWholeHistoryNotJustTheWindow
// pins the fix for already-drawn parts of the graph visibly rescaling
// every tick: the scale must anchor to the whole session history, not
// just whatever's currently in the visible window. Here the true peak
// (5000) is old enough to have scrolled out of the window (width*2 = 8
// sub-columns; only the trailing flat 100s are ever drawn) — it must
// still set the scale. If the scale were recomputed from only what's
// visible, the flat 100s would each be their own local peak and pin the
// line at the very top instead of low, near the baseline.
// rowsWithInk reports which rendered rows carry any braille dot, top first.
func rowsWithInk(rows []string) []int {
	var with []int
	for i, row := range rows {
		for _, r := range ansi.Strip(row) {
			if r > 0x2800 && r <= 0x28FF { // 0x2800 is the blank cell
				with = append(with, i)
				break
			}
		}
	}
	return with
}

// A peak that has only just scrolled off the left edge still sets the
// ceiling. Otherwise the whole graph would rescale taller and hotter the
// instant it went, every tick.
func TestRenderThroughputLineKeepsTheCeilingOfAJustDepartedPeak(t *testing.T) {
	// width 4 -> 8 sub-columns visible, 16 within the peak lookback.
	samples := make([]int64, 12)
	samples[0] = 5000
	for i := 1; i < len(samples); i++ {
		samples[i] = 100
	}

	rows := renderThroughputLine(samples, 4, 3)

	top := ansi.Strip(rows[0])
	for _, r := range top {
		if r != 0x2800 {
			t.Fatalf("top row = %q, want blank — a flat low value must not be pinned at the top just because the recent peak has left the window", top)
		}
	}
}

// But a peak long gone must stop setting it, or one early spike flattens
// every later transfer into the bottom row for the rest of the connection.
func TestRenderThroughputLineRecoversFromALongGonePeak(t *testing.T) {
	samples := make([]int64, 40) // well beyond the 16-sample lookback
	samples[0] = 5000
	for i := 1; i < len(samples); i++ {
		samples[i] = 100
	}

	rows := renderThroughputLine(samples, 4, 3)

	if len(rowsWithInk(rows)) == 0 {
		t.Fatal("nothing drawn at all")
	}
	if ink := rowsWithInk(rows); ink[0] != 0 {
		t.Fatalf("ink starts on row %d, want row 0 — steady traffic should use the graph's full height once an ancient spike has aged out", ink[0])
	}
}

// The symptom that prompted this: with sampling now running for the life of
// the connection, history outlives the view, and an early spike used to
// flatten everything after it so no peaks were drawn at all.
func TestRenderThroughputLineStillShowsPeaksAfterAnEarlySpike(t *testing.T) {
	const width, height = 60, 8
	subWidth := width * 2

	varying := make([]int64, subWidth)
	for i := range varying {
		if i%20 < 10 {
			varying[i] = 200
		} else {
			varying[i] = 2000
		}
	}
	clean := rowsWithInk(renderThroughputLine(varying, width, height))

	withSpike := append([]int64{50000}, make([]int64, 600)...)
	withSpike = append(withSpike, varying...)
	after := rowsWithInk(renderThroughputLine(withSpike, width, height))

	if len(clean) < height {
		t.Fatalf("the control case only used rows %v; this test cannot detect flattening", clean)
	}
	if len(after) != len(clean) {
		t.Fatalf("an old spike flattened the graph to rows %v, want the full range %v", after, clean)
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

func TestPercentDoneIsZeroWithNothingToMeasure(t *testing.T) {
	snap := statsSnapshot{}
	if got := snap.percentDone(); got != 0 {
		t.Fatalf("percentDone with totalBytes 0 = %v, want 0", got)
	}
}

func TestPercentDoneIsBytesTransferredOverTotalBytes(t *testing.T) {
	snap := statsSnapshot{bytesTransferred: 25, totalBytes: 100}
	if got := snap.percentDone(); got != 25 {
		t.Fatalf("percentDone = %v, want 25", got)
	}
}
