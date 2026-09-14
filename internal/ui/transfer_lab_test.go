package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"tideftp/internal/domain"
)

func TestTransferLabQueuesSyntheticTinyFileStorm(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.transferLab = true
	model.openTransferLab()
	if model.overlay != overlayTransferLab {
		t.Fatalf("overlay = %v, want Transfer Lab", model.overlay)
	}

	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if len(model.transfers) != transferLabScenarios[0].files {
		t.Fatalf("lab queued %d transfers, want %d", len(model.transfers), transferLabScenarios[0].files)
	}
	if model.transfers[0].Status != domain.Active || !strings.Contains(model.transfers[0].Source, "Tiny-file storm") {
		t.Fatalf("first lab transfer = %+v, want an active Tiny-file scenario row", model.transfers[0])
	}
}

func TestTransferLabIsHiddenWithoutDeveloperMode(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	for _, command := range model.paletteCommands() {
		if command.id == commandTransferLab {
			t.Fatal("Transfer Lab leaked into the normal command palette")
		}
	}
	model.openTransferLab()
	if model.overlay == overlayTransferLab || !strings.Contains(model.status, "--transfer-lab") {
		t.Fatalf("normal model opened Transfer Lab: overlay=%v status=%q", model.overlay, model.status)
	}
}

func TestTransferLabOverlayExplainsItsSafetyBoundary(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.transferLab = true
	model.openTransferLab()
	plain := ansi.Strip(model.View())
	for _, want := range []string{"transfer lab", "Developer-only", "Tiny-file storm", "Drop mid-transfer", "no real files or server"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("Transfer Lab overlay missing %q:\n%s", want, plain)
		}
	}
}
