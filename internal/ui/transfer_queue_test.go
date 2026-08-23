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
