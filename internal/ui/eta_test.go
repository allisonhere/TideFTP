package ui

import (
	"strings"
	"testing"
	"time"

	"tideftp/internal/domain"
)

// etaNow is the fixed "now" every test here measures against, so a rate is
// arithmetic rather than a race with the wall clock.
var etaNow = time.Date(2026, 1, 15, 9, 30, 10, 0, time.UTC)

func activeTransfer(done, total int64, startedSecondsAgo int) domain.Transfer {
	return domain.Transfer{
		ID:         1,
		Status:     domain.Active,
		BytesDone:  done,
		BytesTotal: total,
		StartedAt:  etaNow.Add(-time.Duration(startedSecondsAgo) * time.Second),
	}
}

func TestTransferETAFromTheAverageRate(t *testing.T) {
	// 1 MB moved in 10s = 100 KB/s; 2 MB left is 20s.
	row := activeTransfer(1_000_000, 3_000_000, 10)

	eta, ok := transferETA(row, etaNow)
	if !ok {
		t.Fatalf("no ETA for a transfer with measurable progress")
	}
	if got := eta.Round(time.Second); got != 20*time.Second {
		t.Fatalf("eta = %v, want 20s", got)
	}
}

func TestTransferETAExcludesResumedBytes(t *testing.T) {
	// Resumed at 900 KB, so only 100 KB is this transfer's own work: 10 KB/s
	// over 10s, and the remaining 1 MB takes 100s — not the 10s a rate that
	// counted the resume offset would claim.
	row := activeTransfer(1_000_000, 2_000_000, 10)
	row.ResumeFrom = 900_000

	eta, ok := transferETA(row, etaNow)
	if !ok {
		t.Fatalf("no ETA for a resumed transfer that is making progress")
	}
	if got := eta.Round(time.Second); got != 100*time.Second {
		t.Fatalf("eta = %v, want 100s — bytes already there are not this transfer's work", got)
	}
}

func TestTransferETAIsUnknowableWithoutProgressOrSize(t *testing.T) {
	cases := map[string]domain.Transfer{
		"not started":  {Status: domain.Active, BytesTotal: 100},
		"no bytes yet": activeTransfer(0, 1000, 5),
		"no size":      activeTransfer(500, 0, 5),
		"queued":       {Status: domain.Queued, BytesTotal: 1000, StartedAt: etaNow.Add(-time.Second)},
	}
	for name, row := range cases {
		if _, ok := transferETA(row, etaNow); ok {
			t.Errorf("%s: reported an ETA it cannot know", name)
		}
	}
}

func TestOverallETACountsQueuedWorkAtTheActiveRate(t *testing.T) {
	model := Model{transfers: []domain.Transfer{
		activeTransfer(1_000_000, 2_000_000, 10), // 100 KB/s, 1 MB left
		{Status: domain.Queued, BytesTotal: 1_000_000},
		{Status: domain.Done, BytesTotal: 5_000_000, BytesDone: 5_000_000},
	}}

	eta, ok := model.overallETA(etaNow)
	if !ok {
		t.Fatalf("no overall ETA with one transfer running and one queued")
	}
	// 2 MB outstanding at 100 KB/s.
	if got := eta.Round(time.Second); got != 20*time.Second {
		t.Fatalf("overall eta = %v, want 20s", got)
	}
}

func TestOverallETAIsSilentWithNothingMoving(t *testing.T) {
	model := Model{transfers: []domain.Transfer{
		{Status: domain.Queued, BytesTotal: 1_000_000},
		{Status: domain.Done, BytesTotal: 10, BytesDone: 10},
	}}

	if _, ok := model.overallETA(etaNow); ok {
		t.Fatalf("a queue with nothing running has no rate to estimate from")
	}
	if hint := model.bottomPaneHint(etaNow); strings.Contains(hint, "ETA") {
		t.Fatalf("hint = %q, want no ETA when there is nothing to measure", hint)
	}
}

func TestFormatETA(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0:00"},
		{7 * time.Second, "0:07"},
		{83 * time.Second, "1:23"},
		{time.Hour + 2*time.Minute + 33*time.Second, "1:02:33"},
		{48 * time.Hour, "1d+"},
	}
	for _, c := range cases {
		if got := formatETA(c.in); got != c.want {
			t.Errorf("formatETA(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTransferRowShowsRateAndETAWhileRunning(t *testing.T) {
	row := activeTransfer(1_000_000, 3_000_000, 10)

	label := transferMetaLabel(row, etaNow)

	if !strings.Contains(label, "/s") || !strings.Contains(label, "ETA 0:20") {
		t.Fatalf("meta = %q, want a throughput and an ETA", label)
	}
}

func TestTransferRowFallsBackToItsMessage(t *testing.T) {
	row := domain.Transfer{Status: domain.Queued, Message: "queued"}

	if got := transferMetaLabel(row, etaNow); got != "queued" {
		t.Fatalf("meta = %q, want the row's own message when there is no rate", got)
	}
}
