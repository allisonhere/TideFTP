package ui

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
)

// updateGolden regenerates every golden file instead of comparing against
// it. Review the diff before committing: `go test ./internal/ui/... -run
// TestGolden -update`.
var updateGolden = flag.Bool("update", false, "update golden files")

// assertGolden compares got against testdata/<name>.golden, ANSI stripped
// so the file stays plain, readable text that doesn't churn across themes
// or terminal color profiles.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run `go test ./internal/ui/... -run %s -update` to create it)", path, err, t.Name())
	}
	if got != string(want) {
		t.Errorf("%s does not match %s — run `go test ./internal/ui/... -run %s -update` to review and accept the change\n\n--- got ---\n%s\n--- want ---\n%s",
			t.Name(), path, t.Name(), got, string(want))
	}
}

// goldenModel builds a fully deterministic connected model: fixed local and
// remote entries (a real fakefs/localfs listing carries a wall-clock
// "Modified" time that would make the rendered date column, and so every
// golden file, drift day to day) and a fixed mix of transfer states.
func goldenModel(t *testing.T) Model {
	t.Helper()
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model.width, model.height = 100, 30

	fixed := time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC)
	model.local.path = "/home/allie/projects"
	model.local.entries = []domain.Entry{
		{Name: "assets", Kind: domain.EntryDir, Mode: "drwxr-xr-x", Modified: fixed},
		{Name: "README.md", Kind: domain.EntryFile, Size: 2048, Mode: "-rw-r--r--", Modified: fixed},
		{Name: "main.go", Kind: domain.EntryFile, Size: 4096, Mode: "-rw-r--r--", Modified: fixed},
	}
	model.local.cursor, model.local.offset = 0, 0

	model.remote.path = "/public_html"
	model.remote.entries = []domain.Entry{
		{Name: "uploads", Kind: domain.EntryDir, Mode: "drwxr-xr-x", Modified: fixed},
		{Name: "index.html", Kind: domain.EntryFile, Size: 8192, Mode: "-rw-r--r--", Modified: fixed},
	}
	model.remote.cursor, model.remote.offset = 0, 0

	model.transfers = []domain.Transfer{
		{ID: 1, Direction: domain.Upload, Source: "/home/allie/projects/main.go", Destination: "/public_html/main.go", BytesTotal: 4096, BytesDone: 2048, Status: domain.Active, Message: "transferring"},
		{ID: 2, Direction: domain.Download, Source: "/public_html/index.html", Destination: "/home/allie/projects/index.html", BytesTotal: 8192, Status: domain.Queued, Message: "queued"},
	}
	return model
}

func TestGoldenMainScreen(t *testing.T) {
	model := goldenModel(t)
	assertGolden(t, "main_screen", ansi.Strip(model.View()))
}

func TestGoldenHelpOverlay(t *testing.T) {
	model := goldenModel(t)
	model.overlay = overlayHelp
	assertGolden(t, "help_overlay", ansi.Strip(model.View()))
}

func TestGoldenConnectFormOverlay(t *testing.T) {
	model := goldenModel(t)
	model = press(t, model, runes("c"))
	assertGolden(t, "connect_form_overlay", ansi.Strip(model.View()))
}

func TestGoldenSettingsOverlay(t *testing.T) {
	model := goldenModel(t)
	model.overlay = overlaySettings
	model.settingsCursor = 0
	assertGolden(t, "settings_overlay", ansi.Strip(model.View()))
}

func TestGoldenConflictOverlay(t *testing.T) {
	model := goldenModel(t)
	model.overlay = overlayConflict
	assertGolden(t, "conflict_overlay", ansi.Strip(model.View()))
}

func TestGoldenPreflightOverlay(t *testing.T) {
	model := goldenModel(t)
	model.overlay = overlayPreflight
	model.preflight = &preflightScan{
		direction:  domain.Download,
		files:      make([]preflightFile, 8),
		folders:    3,
		totalBytes: 4447702,
	}
	assertGolden(t, "preflight_overlay", ansi.Strip(model.View()))
}
