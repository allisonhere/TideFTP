package ui

import (
	"testing"
	"time"

	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
	"tideftp/internal/faketransfer"
	"tideftp/internal/localfs"
)

// TestRealEngineDrivesTheQueue wires the real fake engine to the real model,
// with no scripted stub in between, and pumps its events the way the Bubble
// Tea runtime would. It covers the whole path: queue -> Engine.Start -> engine
// goroutines -> events -> queue state, including a freed slot letting the next
// queued transfer start.
func TestRealEngineDrivesTheQueue(t *testing.T) {
	engine := faketransfer.NewWithInterval(time.Millisecond)
	defer engine.Close()

	model := loadedModelOver(t, localfs.New(), fakefs.NewRemote(), engine)
	// selectAll acts on the focused pane, so focus must be on the remote one.
	model.focus = focusRemote

	download := func(remotePath string) {
		model = settle(t, model, model.navigateTo(paneRemote, remotePath))
		if model.remote.path != remotePath {
			t.Fatalf("navigating to %q left the pane at %q", remotePath, model.remote.path)
		}
		model.selectAll()
		model = press(t, model, runes("d"))
	}

	// Five files across two directories, so IDs run 1..5 and the engine's
	// every-fifth failure rule puts exactly one transfer in the Failed tab.
	download("/public_html/assets")
	download("/public_html/uploads")

	if len(model.transfers) != 5 {
		t.Fatalf("queued %d transfers, want 5", len(model.transfers))
	}
	if got := countStatus(model.transfers, domain.Active); got != model.maxParallel {
		t.Fatalf("%d transfers started at once, want the cap of %d", got, model.maxParallel)
	}

	deadline := time.After(20 * time.Second)
	for !allFinished(model.transfers) {
		select {
		case event, ok := <-engine.Events():
			if !ok {
				t.Fatalf("engine closed its stream with transfers still running")
			}
			next, _ := model.Update(event)
			model = next.(Model)
		case <-deadline:
			t.Fatalf("timed out; queue still %v", statuses(model.transfers))
		}
	}

	if got := countStatus(model.transfers, domain.Done); got != 4 {
		t.Fatalf("completed %d transfers, want 4 (statuses %v)", got, statuses(model.transfers))
	}
	if got := countStatus(model.transfers, domain.Failed); got != 1 {
		t.Fatalf("failed %d transfers, want 1 (statuses %v)", got, statuses(model.transfers))
	}

	for _, row := range model.transfers {
		switch row.Status {
		case domain.Done:
			if row.BytesDone != row.BytesTotal {
				t.Fatalf("transfer %d done at %d/%d bytes", row.ID, row.BytesDone, row.BytesTotal)
			}
		case domain.Failed:
			if row.Message == "" {
				t.Fatalf("transfer %d failed without a reason", row.ID)
			}
			if row.BytesDone >= row.BytesTotal {
				t.Fatalf("transfer %d failed but reported every byte moved", row.ID)
			}
		}
	}

	model.bottomTab = tabFailed
	if got := model.bottomRowCount(); got != 1 {
		t.Fatalf("failed tab shows %d rows, want 1", got)
	}
	model.bottomTab = tabHistory
	if got := model.bottomRowCount(); got != 4 {
		t.Fatalf("history tab shows %d rows, want 4", got)
	}
}

// TestRealEngineCancelStopsTheQueue checks cancel reaches real engine
// goroutines and comes back as terminal events.
func TestRealEngineCancelStopsTheQueue(t *testing.T) {
	engine := faketransfer.NewWithInterval(20 * time.Millisecond)
	defer engine.Close()

	model := loadedModelOver(t, localfs.New(), fakefs.NewRemote(), engine)
	model.transfers = []domain.Transfer{
		{ID: 1, BytesTotal: 1 << 30, Status: domain.Queued},
		{ID: 2, BytesTotal: 1 << 30, Status: domain.Queued},
	}
	model.startQueuedTransfers()

	model = press(t, model, runes("x"))

	deadline := time.After(20 * time.Second)
	for !allFinished(model.transfers) {
		select {
		case event := <-engine.Events():
			next, _ := model.Update(event)
			model = next.(Model)
		case <-deadline:
			t.Fatalf("cancel never landed; queue still %v", statuses(model.transfers))
		}
	}
	for _, row := range model.transfers {
		if row.Message != "canceled" {
			t.Fatalf("transfer %d message = %q, want canceled", row.ID, row.Message)
		}
	}
}

func allFinished(transfers []domain.Transfer) bool {
	for _, row := range transfers {
		if row.Status == domain.Queued || row.Status == domain.Active {
			return false
		}
	}
	return true
}

func statuses(transfers []domain.Transfer) []string {
	out := make([]string, 0, len(transfers))
	for _, row := range transfers {
		out = append(out, transferStatus(row.Status))
	}
	return out
}
