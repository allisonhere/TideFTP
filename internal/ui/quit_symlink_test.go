package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
)

func isQuit(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// Quitting mid-transfer leaves a partial file at whichever end was being
// written, with nothing on screen to say so. It is worth one keypress.
func TestQuitAsksWhileTransfersAreRunning(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.transfers = []domain.Transfer{
		{ID: 1, Status: domain.Active, Source: "/src/big.iso", Destination: "/dst/big.iso"},
		{ID: 2, Status: domain.Queued, Source: "/src/next.iso", Destination: "/dst/next.iso"},
	}

	updated, cmd := model.updateKey(runes("q"))
	model = updated.(Model)

	if isQuit(t, cmd) {
		t.Fatal("q quit outright with transfers still running")
	}
	if model.overlay != overlayQuitConfirm {
		t.Fatalf("overlay = %v, want overlayQuitConfirm", model.overlay)
	}
	// The confirmation has to render, not just exist.
	if view := model.View(); !strings.Contains(view, "Quit with transfers still running?") {
		t.Fatal("the quit confirmation does not appear in the view")
	}

	// Declining leaves the queue alone.
	updated, _ = model.updateKey(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.overlay != overlayNone || len(model.transfers) != 2 {
		t.Fatal("declining the quit should dismiss the overlay and keep the queue")
	}
}

func TestQuitConfirmedGoesThrough(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.transfers = []domain.Transfer{{ID: 1, Status: domain.Active}}
	model.overlay = overlayQuitConfirm

	_, cmd := model.updateKey(runes("y"))
	if cmd == nil {
		t.Fatal("confirming the quit did nothing")
	}
}

// An idle queue has nothing to lose, so q must not grow an extra keypress.
func TestQuitWithAnIdleQueueDoesNotAsk(t *testing.T) {
	model := droppedModel(t)

	updated, cmd := model.updateKey(runes("q"))
	if updated.(Model).overlay == overlayQuitConfirm {
		t.Fatal("an idle queue must not raise the quit confirmation")
	}
	if !isQuit(t, cmd) {
		t.Fatal("q with an idle queue should quit")
	}
}

// ctrl+c is the reflex for "get me out of here". An overlay that swallows it
// reads as a hung app, and every overlay used to swallow it.
func TestCtrlCQuitsFromInsideAnOverlay(t *testing.T) {
	for name, overlay := range map[string]overlayMode{
		"help":        overlayHelp,
		"connect":     overlayConnect,
		"settings":    overlaySettings,
		"palette":     overlayCommandPalette,
		"file action": overlayFileAction,
		"server list": overlayServerList,
		"sync":        overlaySync,
		"conflict":    overlayConflict,
	} {
		t.Run(name, func(t *testing.T) {
			model := droppedModel(t)
			model.overlay = overlay

			_, cmd := model.updateKey(tea.KeyMsg{Type: tea.KeyCtrlC})
			if !isQuit(t, cmd) {
				t.Fatalf("ctrl+c inside the %s overlay did not quit", name)
			}
		})
	}
}

func TestCtrlCForcesQuitFromTheConfirmation(t *testing.T) {
	model := droppedModel(t)
	model.transfers = []domain.Transfer{{ID: 1, Status: domain.Active}}
	model.overlay = overlayQuitConfirm

	_, cmd := model.updateKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !isQuit(t, cmd) {
		t.Fatal("ctrl+c from the quit confirmation must quit without asking again")
	}
}

// Enter on a symlinked directory used to do nothing at all, with no message —
// which reads as a broken key on exactly the layouts this app is for.
func TestEnterOpensASymlinkedDirectory(t *testing.T) {
	local := newSyncFS()
	link := domain.Entry{Name: "www", Kind: domain.EntrySymlink, LinksToDir: true, Mode: "lrwxrwxrwx"}
	local.put("/src", link)
	local.tree["/src/www"] = []domain.Entry{file("index.html", 12, time.Time{})}

	model, _ := mirrorModel(t, local, newSyncFS())
	model.local.entries = []domain.Entry{link}
	model.local.allEntries = model.local.entries
	model.local.cursor = 0

	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if model.local.path != "/src/www" {
		t.Fatalf("local pane at %q, want /src/www — enter did not follow the link", model.local.path)
	}
}

// A symlink to a plain file is not a directory and must not become navigable.
func TestEnterIgnoresASymlinkToAFile(t *testing.T) {
	local := newSyncFS()
	link := domain.Entry{Name: "current.log", Kind: domain.EntrySymlink}
	local.put("/src", link)

	model, _ := mirrorModel(t, local, newSyncFS())
	model.local.entries = []domain.Entry{link}
	model.local.allEntries = model.local.entries
	model.local.cursor = 0

	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if model.local.path != "/src" {
		t.Fatalf("local pane moved to %q on a symlink to a file", model.local.path)
	}
}

// Queuing a symlinked directory used to produce a transfer that failed at
// open — transfer.Engine moves file bytes and vfs.FS cannot recreate a link.
// Following it instead is worse: a link back up its own tree would drive the
// walk until it hit the cap.
func TestPreflightSkipsSymlinkedDirectoriesRatherThanQueueingThem(t *testing.T) {
	src, dst := newSyncFS(), newSyncFS()
	link := domain.Entry{Name: "www", Kind: domain.EntrySymlink, LinksToDir: true}
	entries := []domain.Entry{file("real.txt", 4, time.Time{}), link}
	src.put("/src", entries...)
	src.tree["/src/www"] = []domain.Entry{file("looped.txt", 9, time.Time{})}
	dst.put("/dst")

	model, _ := mirrorModel(t, src, dst)
	msg := model.beginPreflightScan(domain.Upload, entries, "/src", "/dst", src, dst, true)().(preflightScanMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}

	if msg.scan.skippedLinks != 1 {
		t.Fatalf("skippedLinks = %d, want 1", msg.scan.skippedLinks)
	}
	if len(msg.scan.files) != 1 || msg.scan.files[0].name != "real.txt" {
		t.Fatalf("files = %+v, want only real.txt", msg.scan.files)
	}

	model.commitScan(msg.scan)
	if len(model.transfers) != 1 {
		t.Fatalf("queued %d transfers, want only the real file", len(model.transfers))
	}
	if !strings.Contains(model.status, "skipped 1 linked folder") {
		t.Fatalf("status = %q, want it to mention the skipped link", model.status)
	}
}

// Prune is the one operation here that destroys data, and a truncated scan
// never saw the whole source tree — so "has no source counterpart" is not
// something it can claim about anything.
func TestPruneCannotBeArmedOnATruncatedScan(t *testing.T) {
	model, _ := mirrorModel(t, newSyncFS(), newSyncFS())
	model.sync = &syncPlan{
		truncated:  true,
		prunePaths: []prunePath{{path: "/dst/extra", name: "extra"}},
	}
	model.overlay = overlaySync

	model = press(t, model, runes("p"))

	if model.sync.prune {
		t.Fatal("prune was armed on a scan that never finished")
	}
	if !model.statusErr || !strings.Contains(model.status, "complete scan") {
		t.Fatalf("status = %q, want an explanation", model.status)
	}
}

// The same guard again at the point of no return, in case a plan reaches
// confirmSync already armed.
func TestConfirmSyncRefusesToPruneATruncatedPlan(t *testing.T) {
	dst := newSyncFS()
	dst.put("/dst", file("extra", 1, time.Time{}))
	model, _ := mirrorModel(t, newSyncFS(), dst)
	model.sync = &syncPlan{
		dstFS:      dst,
		truncated:  true,
		prune:      true,
		prunePaths: []prunePath{{path: "/dst/extra", name: "extra"}},
	}

	if cmd := model.confirmSync(); cmd != nil {
		t.Fatal("a truncated plan must not issue a prune")
	}
	if len(dst.tree["/dst"]) != 1 {
		t.Fatal("the extra file was deleted on a partial picture")
	}
}
