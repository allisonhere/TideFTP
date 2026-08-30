package ui

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
	"tideftp/internal/session"
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
	got = trimGoldenLinePadding(got)
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
	wantText := trimGoldenLinePadding(string(want))
	if got != wantText {
		t.Errorf("%s does not match %s — run `go test ./internal/ui/... -run %s -update` to review and accept the change\n\n--- got ---\n%s\n--- want ---\n%s",
			t.Name(), path, t.Name(), got, wantText)
	}
}

func trimGoldenLinePadding(value string) string {
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
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

func TestGoldenServerListOverlay(t *testing.T) {
	model := goldenModel(t)
	model.profiles = []session.Target{
		{Name: "prod web", Protocol: "sftp", Host: "web1.example.com", Port: 22, User: "deploy", StartPath: "/srv/www"},
		{Name: "backups", Protocol: "ftp", Host: "nas.local", Port: 21, User: "bob", StartPath: "/"},
	}
	model = press(t, model, runes("c"))
	assertGolden(t, "server_list_overlay", ansi.Strip(model.View()))
}

func TestGoldenSettingsOverlay(t *testing.T) {
	model := goldenModel(t)
	model.overlay = overlaySettings
	model.settingsCursor = 0
	// Pin the Editor row: its "auto (…)" label is whatever this machine has.
	model.editorSetting = "vi"
	assertGolden(t, "settings_overlay", ansi.Strip(model.View()))
}

func TestGoldenPreviewOverlay(t *testing.T) {
	model := goldenModel(t)
	model.overlay = overlayPreview
	body := []byte("# Deploy notes\n\n- push the build\n- restart the service\n- tail the log\n")
	preview := newPreviewState("deploy-notes.md", "/releases/deploy-notes.md", int64(len(body)), body, false)
	model.preview = &preview
	assertGolden(t, "preview_overlay", ansi.Strip(model.View()))
}

func TestGoldenPreviewOverlayHex(t *testing.T) {
	model := goldenModel(t)
	model.overlay = overlayPreview
	body := []byte{0x7f, 'E', 'L', 'F', 0x02, 0x01, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x02, 0x00, 0x3e, 0x00, 0x01, 0x00, 0x00, 0x00}
	preview := newPreviewState("tideftp", "/releases/2026-08-ship/tideftp", 6812440, body, true)
	model.preview = &preview
	assertGolden(t, "preview_overlay_hex", ansi.Strip(model.View()))
}

func TestGoldenConflictOverlay(t *testing.T) {
	model := goldenModel(t)
	model.overlay = overlayConflict
	entry := domain.Entry{Name: "index.html", Size: 8192}
	model.preflight = &preflightScan{
		direction: domain.Upload,
		files:     []preflightFile{{src: "/home/allie/projects/index.html", dst: "/public_html/index.html", name: "index.html", size: 8192, conflict: &entry}},
		cursor:    int(conflictOverwrite),
	}
	assertGolden(t, "conflict_overlay", ansi.Strip(model.View()))
}

func TestGoldenHostKeyOverlay(t *testing.T) {
	model := goldenModel(t)
	model.overlay = overlayHostKey
	model.hostKeyPrompt = &hostKeyPrompt{
		target: testTarget,
		err: &session.UntrustedHostKeyError{
			Address:     testTarget.Address(),
			Algorithm:   "ssh-ed25519",
			Fingerprint: "SHA256:AAAAC3NzaC1lZDI1NTE5AAAAIExampleFingerprintOnly",
		},
	}
	assertGolden(t, "host_key_overlay", ansi.Strip(model.View()))
}

func TestGoldenStatsTab(t *testing.T) {
	model := goldenModel(t)
	model.bottomTab = tabStats
	fixed := time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC)
	model.transfers = []domain.Transfer{
		{ID: 1, Status: domain.Active, BytesDone: 2048, BytesTotal: 4096, Protocol: "sftp"},
		{ID: 2, Status: domain.Queued, BytesTotal: 8192, Protocol: "sftp"},
		{ID: 3, Status: domain.Done, BytesDone: 100000, BytesTotal: 100000, StartedAt: fixed, FinishedAt: fixed.Add(10 * time.Second), Protocol: "sftp"},
		{ID: 4, Status: domain.Done, BytesDone: 50000, BytesTotal: 50000, StartedAt: fixed, FinishedAt: fixed.Add(5 * time.Second), Protocol: "ftp"},
		{ID: 5, Status: domain.Failed, BytesDone: 512, BytesTotal: 20000, Protocol: "ftp"},
	}
	model.stats = model.computeStats()
	model.stats.currentThroughput = 4300000
	model.statsHistory = []int64{0, 100000, 500000, 2000000, 4300000, 3800000, 2100000, 900000, 200000, 0}
	assertGolden(t, "stats_tab", ansi.Strip(model.View()))
}

// TestGoldenStatsTabWithGraph covers the tab at a bottom-pane size tall
// enough to actually show renderThroughputGraph's output, not just the
// no-room-for-it fallback TestGoldenStatsTab exercises at the default
// split.
func TestGoldenStatsTabWithGraph(t *testing.T) {
	model := goldenModel(t)
	model.bottomTab = tabStats
	model.height = 46
	fixed := time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC)
	model.transfers = []domain.Transfer{
		{ID: 1, Status: domain.Active, BytesDone: 2048, BytesTotal: 4096, Protocol: "sftp"},
		{ID: 2, Status: domain.Done, BytesDone: 100000, BytesTotal: 100000, StartedAt: fixed, FinishedAt: fixed.Add(10 * time.Second), Protocol: "sftp"},
		{ID: 3, Status: domain.Failed, BytesDone: 512, BytesTotal: 20000, Protocol: "ftp"},
	}
	model.stats = model.computeStats()
	model.stats.currentThroughput = 4300000
	model.statsHistory = []int64{0, 100000, 500000, 2000000, 4300000, 3800000, 2100000, 900000, 200000, 0}
	assertGolden(t, "stats_tab_with_graph", ansi.Strip(model.View()))
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
