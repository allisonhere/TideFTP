package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/config"
	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
	"tideftp/internal/localfs"
	"tideftp/internal/session"
	"tideftp/internal/transfer"
)

func TestAdjustMaxParallelClampsAtOneAndTheCap(t *testing.T) {
	var saved []config.Config
	save := func(c config.Config) error { saved = append(saved, c); return nil }
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, []session.Target{testTarget}, config.Default(), save, nil)
	model.width, model.height = 120, 36

	if model.maxParallel != defaultParallelTransfers {
		t.Fatalf("maxParallel = %d, want the default %d", model.maxParallel, defaultParallelTransfers)
	}

	model = press(t, model, runes("-"))
	if model.maxParallel != defaultParallelTransfers-1 {
		t.Fatalf("maxParallel after - = %d, want %d", model.maxParallel, defaultParallelTransfers-1)
	}
	for i := 0; i < 10; i++ {
		model = press(t, model, runes("-"))
	}
	if model.maxParallel != 1 {
		t.Fatalf("maxParallel floor = %d, want 1", model.maxParallel)
	}

	for i := 0; i < maxParallelCap+5; i++ {
		model = press(t, model, runes("+"))
	}
	if model.maxParallel != maxParallelCap {
		t.Fatalf("maxParallel ceiling = %d, want %d", model.maxParallel, maxParallelCap)
	}

	if len(saved) == 0 || saved[len(saved)-1].MaxParallel != maxParallelCap {
		t.Fatalf("adjusting parallelism was not persisted: saved = %+v", saved)
	}
}

func TestAdjustMaxParallelStartsWaitingQueuedTransfers(t *testing.T) {
	engine := newScriptedEngine()
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: engine})
	model.maxParallel = 1
	model.transfers = []domain.Transfer{
		{ID: 1, Status: domain.Active, Source: "/a", Destination: "/a"},
		{ID: 2, Status: domain.Queued, Source: "/b", Destination: "/b", BytesTotal: 10},
	}

	updated, cmd := model.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")})
	model = updated.(Model)
	if cmd != nil {
		model = settle(t, model, cmd)
	}

	if model.transfers[1].Status != domain.Active {
		t.Fatalf("raising maxParallel did not start the waiting queued transfer: %+v", model.transfers[1])
	}
	if len(engine.started) != 1 || engine.started[0].ID != 2 {
		t.Fatalf("engine.started = %+v, want transfer 2 to have been started", engine.started)
	}
}

func TestParallelismShownInTheQueueTab(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model.maxParallel = 3

	view := model.View()
	if !strings.Contains(view, "(3x)") {
		t.Fatalf("queue tab does not show the current parallelism:\n%s", view)
	}
}

func TestXOnAnActiveRowCancelsOnlyThatTransfer(t *testing.T) {
	engine := newScriptedEngine()
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: engine})
	model.focus = focusQueue
	model.bottomTab = tabQueue
	model.transfers = []domain.Transfer{
		{ID: 1, Status: domain.Active, Source: "/a", Destination: "/a"},
		{ID: 2, Status: domain.Active, Source: "/b", Destination: "/b"},
	}
	model.bottomCursor = 1

	model = press(t, model, runes("x"))

	if len(engine.canceled) != 1 || engine.canceled[0] != 2 {
		t.Fatalf("engine.canceled = %v, want just transfer 2 (the one under the cursor)", engine.canceled)
	}
}

func TestXOnAQueuedRowDropsItWithoutTouchingTheEngine(t *testing.T) {
	engine := newScriptedEngine()
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: engine})
	model.focus = focusQueue
	model.bottomTab = tabQueue
	model.transfers = []domain.Transfer{
		{ID: 1, Status: domain.Active, Source: "/a", Destination: "/a"},
		{ID: 2, Status: domain.Queued, Source: "/b", Destination: "/b"},
	}
	model.bottomCursor = 1

	model = press(t, model, runes("x"))

	if len(engine.canceled) != 0 {
		t.Fatalf("engine.canceled = %v, a Queued transfer was never started and needs no engine call", engine.canceled)
	}
	if len(model.transfers) != 1 || model.transfers[0].ID != 1 {
		t.Fatalf("transfers = %+v, want only transfer 1 left", model.transfers)
	}
}

func TestXUnfocusedStillCancelsEveryActiveTransfer(t *testing.T) {
	engine := newScriptedEngine()
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: engine})
	model.focus = focusLocal
	model.transfers = []domain.Transfer{
		{ID: 1, Status: domain.Active, Source: "/a", Destination: "/a"},
		{ID: 2, Status: domain.Active, Source: "/b", Destination: "/b"},
	}

	model = press(t, model, runes("x"))

	if len(engine.canceled) != 2 {
		t.Fatalf("engine.canceled = %v, want both active transfers (queue pane not focused)", engine.canceled)
	}
}

func TestRetryQueuesAFreshTransferAndKeepsTheFailedRow(t *testing.T) {
	engine := newScriptedEngine()
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: engine})
	model.focus = focusQueue
	model.bottomTab = tabFailed
	model.nextTransferID = 100
	model.transfers = []domain.Transfer{
		{ID: 1, Status: domain.Failed, Direction: domain.Upload, Source: "/a", Destination: "/a", BytesTotal: 500, Message: "boom"},
	}
	model.bottomCursor = 0

	model = press(t, model, runes("R"))

	if len(model.transfers) != 2 {
		t.Fatalf("transfers = %+v, want the original plus a new retry", model.transfers)
	}
	original, retry := model.transfers[0], model.transfers[1]
	if original.Status != domain.Failed || original.Message != "boom" {
		t.Fatalf("original transfer changed: %+v", original)
	}
	if retry.ID == original.ID {
		t.Fatalf("retry reused the original ID %d, want a fresh one", retry.ID)
	}
	if retry.Status != domain.Active && retry.Status != domain.Queued {
		t.Fatalf("retry status = %v, want Queued or Active (started immediately)", retry.Status)
	}
	if retry.Source != original.Source || retry.Destination != original.Destination || retry.BytesTotal != original.BytesTotal {
		t.Fatalf("retry = %+v, want the same source/destination/size as the original", retry)
	}
}

func TestRetryDoesNothingOutsideTheQueuePane(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model.focus = focusLocal
	model.transfers = []domain.Transfer{
		{ID: 1, Status: domain.Failed, Source: "/a", Destination: "/a"},
	}

	model = press(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")})

	if len(model.transfers) != 1 {
		t.Fatalf("transfers = %+v, want R to be a no-op outside the queue pane", model.transfers)
	}
}

func TestRetryOnANonFailedRowDoesNothing(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model.focus = focusQueue
	model.bottomTab = tabActive
	model.transfers = []domain.Transfer{
		{ID: 1, Status: domain.Active, Source: "/a", Destination: "/a"},
	}
	model.bottomCursor = 0

	model = press(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")})

	if len(model.transfers) != 1 {
		t.Fatalf("transfers = %+v, want R on an Active row to be a no-op", model.transfers)
	}
}

func TestADoneTransferAgesOutOfTheQueueTabIntoHistory(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.transfers = []domain.Transfer{{ID: 1, Status: domain.Queued, BytesTotal: 100}}

	next, _ := model.Update(transfer.Event{ID: 1, Kind: transfer.Completed, BytesDone: 100})
	model = next.(Model)
	if model.transfers[0].Status != domain.Done {
		t.Fatalf("status after Completed = %v, want Done", model.transfers[0].Status)
	}

	model.bottomTab = tabQueue
	if rows := model.bottomTabTransfers(); len(rows) != 0 {
		t.Fatalf("Queue tab rows = %+v, want the finished transfer gone", rows)
	}
	model.bottomTab = tabHistory
	if rows := model.bottomTabTransfers(); len(rows) != 1 || rows[0].ID != 1 {
		t.Fatalf("History tab rows = %+v, want the finished transfer", rows)
	}
}

func TestDrainingTheQueueRelistsThePanesSoNewFilesShow(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model = settle(t, model, model.navigateTo(paneRemote, "/public_html"))
	before := model.remote.requestToken

	model.transfers = []domain.Transfer{
		{ID: 1, Status: domain.Active, BytesTotal: 100},
		{ID: 2, Status: domain.Active, BytesTotal: 100},
	}

	// A progress tick never relists.
	next, cmd := model.Update(transfer.Event{ID: 1, Kind: transfer.Progress, BytesDone: 50})
	model = settle(t, next.(Model), cmd)
	if model.remote.requestToken != before {
		t.Fatalf("a progress event relisted the panes")
	}

	// One of two transfers completing leaves the queue busy — still no relist.
	next, cmd = model.Update(transfer.Event{ID: 1, Kind: transfer.Completed, BytesDone: 100})
	model = settle(t, next.(Model), cmd)
	if model.remote.requestToken != before {
		t.Fatalf("relisted while transfer 2 was still active")
	}

	// The last one completing drains the queue and triggers the relist.
	next, cmd = model.Update(transfer.Event{ID: 2, Kind: transfer.Completed, BytesDone: 100})
	model = settle(t, next.(Model), cmd)
	if model.remote.requestToken == before {
		t.Fatalf("queue drained but the panes were never relisted")
	}
}
