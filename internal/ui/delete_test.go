package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"tideftp/internal/domain"
	"tideftp/internal/vfs"
)

// deleteFS is an in-memory vfs.FS for the recursive-delete tests. It is its
// own type rather than a reuse of syncFS for two reasons: Remove here refuses
// a non-empty directory the way a real rmdir does — which is the whole reason
// the walk has to be depth-first — and every method is mutex-guarded, because
// the walk runs on its own goroutine while the test reads the tree.
type deleteFS struct {
	mu   sync.Mutex
	tree map[string][]domain.Entry
	// removed and listed record the walk's traffic, in order.
	removed []string
	listed  []string
	// failList and failRemove make a given path's operation report an error.
	failList   map[string]error
	failRemove map[string]error
	// opDelay is slept before every List and Remove, to give the walk a
	// measurable duration.
	opDelay time.Duration
	// budgets records how long each call's context had left, so a test can
	// show the walk hands out a fresh per-step deadline rather than slicing
	// one budget across the whole thing.
	budgets []time.Duration
}

var _ vfs.FS = (*deleteFS)(nil)

func newDeleteFS() *deleteFS {
	return &deleteFS{
		tree:       map[string][]domain.Entry{},
		failList:   map[string]error{},
		failRemove: map[string]error{},
	}
}

// put registers dir with the given children, making sure every child
// directory has its own (possibly empty) entry so the walk can descend.
func (f *deleteFS) put(dir string, entries ...domain.Entry) *deleteFS {
	dir = syncClean(dir)
	f.tree[dir] = entries
	if parent := f.Parent(dir); parent != dir {
		if _, ok := f.tree[parent]; !ok {
			f.tree[parent] = nil
		}
	}
	for _, e := range entries {
		if e.IsDir() {
			child := syncClean(path.Join(dir, e.Name))
			if _, ok := f.tree[child]; !ok {
				f.tree[child] = nil
			}
		}
	}
	return f
}

// record notes what a call was given, and returns any injected failure.
func (f *deleteFS) record(ctx context.Context, into *[]string, target string, fail map[string]error) error {
	f.mu.Lock()
	if deadline, ok := ctx.Deadline(); ok {
		f.budgets = append(f.budgets, time.Until(deadline))
	}
	*into = append(*into, target)
	delay, err := f.opDelay, fail[target]
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

func (f *deleteFS) List(ctx context.Context, dir string, _ bool) ([]domain.Entry, error) {
	dir = syncClean(dir)
	if err := f.record(ctx, &f.listed, dir, f.failList); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	entries, ok := f.tree[dir]
	if !ok {
		return nil, &fsMissingDirError{dir}
	}
	return append([]domain.Entry(nil), entries...), nil
}

// Remove refuses a non-empty directory, like rmdir and like every real
// vfs.FS adapter.
func (f *deleteFS) Remove(ctx context.Context, target string) error {
	target = syncClean(target)
	if err := f.record(ctx, &f.removed, target, f.failRemove); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if kids, ok := f.tree[target]; ok && len(kids) > 0 {
		return errors.New("directory not empty: " + target)
	}
	parent := f.Parent(target)
	base := path.Base(target)
	siblings := f.tree[parent]
	for i, e := range siblings {
		if e.Name == base {
			f.tree[parent] = append(append([]domain.Entry(nil), siblings[:i]...), siblings[i+1:]...)
			delete(f.tree, target)
			return nil
		}
	}
	return &fsMissingDirError{target}
}

// snapshot returns every path still in the tree that has a parent listing,
// so a test can assert on what survived.
func (f *deleteFS) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for dir, entries := range f.tree {
		for _, e := range entries {
			out = append(out, syncClean(path.Join(dir, e.Name)))
		}
	}
	return out
}

func (f *deleteFS) calls() (listed, removed []string, budgets []time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.listed...), append([]string(nil), f.removed...), append([]time.Duration(nil), f.budgets...)
}

func (f *deleteFS) Child(current, name string) string {
	return syncClean(path.Join(syncClean(current), name))
}
func (f *deleteFS) Parent(current string) string {
	p := path.Dir(syncClean(current))
	if p == "." {
		return "/"
	}
	return p
}
func (f *deleteFS) Mkdir(context.Context, string) error              { return nil }
func (f *deleteFS) Rename(context.Context, string, string) error     { return nil }
func (f *deleteFS) Chmod(context.Context, string, fs.FileMode) error { return nil }
func (f *deleteFS) ReadFile(context.Context, string) ([]byte, error) { return nil, nil }
func (f *deleteFS) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (f *deleteFS) WriteFile(context.Context, string, []byte) error { return nil }

func symlinkToDir(name string) domain.Entry {
	return domain.Entry{Name: name, Kind: domain.EntrySymlink, Mode: "lrwxrwxrwx", LinksToDir: true}
}

// drainDelete reads a job's events to completion and returns the last one,
// which carries the cumulative counts.
func drainDelete(t *testing.T, job *deleteJob) deleteEvent {
	t.Helper()
	var last deleteEvent
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event, ok := <-job.events:
			if !ok {
				return last
			}
			last = event
		case <-deadline:
			t.Fatal("delete job never closed its event stream")
		}
	}
}

// nestedTree is /r with two subdirectories and a file at each level.
func nestedTree() *deleteFS {
	f := newDeleteFS()
	f.put("/", dirEntry("r"))
	f.put("/r", dirEntry("a"), dirEntry("b"), file("top.txt", 10, time.Time{}))
	f.put("/r/a", file("a1.txt", 20, time.Time{}), file("a2.txt", 30, time.Time{}))
	f.put("/r/b", dirEntry("c"))
	f.put("/r/b/c", file("deep.txt", 40, time.Time{}))
	return f
}

func TestDeleteJobRemovesWholeTreeDepthFirst(t *testing.T) {
	f := nestedTree()
	job, _ := startDeleteJob(f, "/", paneRemote, []domain.Entry{dirEntry("r")}, deleteScan{files: 4, folders: 4}, "r")
	last := drainDelete(t, job)

	if left := f.snapshot(); len(left) != 0 {
		t.Fatalf("tree not empty, still holds %v", left)
	}
	if last.failed != 0 {
		t.Fatalf("failed = %d, want 0 (%v)", last.failed, last.err)
	}
	// 4 files + 4 directories (/r, /r/a, /r/b, /r/b/c).
	if last.done != 8 {
		t.Fatalf("done = %d, want 8", last.done)
	}
	_, removed, _ := f.calls()
	// Every directory must come after everything inside it, which is the one
	// property Remove's refusal to drop a non-empty directory depends on.
	for _, pair := range [][2]string{
		{"/r/a/a1.txt", "/r/a"},
		{"/r/a/a2.txt", "/r/a"},
		{"/r/b/c/deep.txt", "/r/b/c"},
		{"/r/b/c", "/r/b"},
		{"/r/top.txt", "/r"},
		{"/r/a", "/r"},
		{"/r/b", "/r"},
	} {
		if indexOf(removed, pair[0]) > indexOf(removed, pair[1]) {
			t.Fatalf("%s removed after its parent %s: %v", pair[0], pair[1], removed)
		}
	}
}

func indexOf(values []string, want string) int {
	for i, v := range values {
		if v == want {
			return i
		}
	}
	return -1
}

// A single unremovable file used to strand every item after it. It must now
// be counted and stepped over.
func TestDeleteJobContinuesPastAFailure(t *testing.T) {
	f := nestedTree()
	f.failRemove["/r/a/a1.txt"] = errors.New("permission denied")

	job, _ := startDeleteJob(f, "/", paneRemote, []domain.Entry{dirEntry("r")}, deleteScan{files: 4, folders: 4}, "r")
	last := drainDelete(t, job)

	// a1.txt survives, and so do the two directories above it, which cannot
	// be emptied while it is there. Everything on the other branches goes.
	left := f.snapshot()
	want := map[string]bool{"/r": true, "/r/a": true, "/r/a/a1.txt": true}
	if len(left) != len(want) {
		t.Fatalf("survivors = %v, want %v", left, want)
	}
	for _, p := range left {
		if !want[p] {
			t.Fatalf("survivors = %v, want %v", left, want)
		}
	}
	if last.done != 5 {
		t.Fatalf("done = %d, want 5", last.done)
	}
	if last.failed != 3 {
		t.Fatalf("failed = %d, want 3 (the file, plus /r/a and /r that it keeps non-empty)", last.failed)
	}
	if last.err == nil || !strings.Contains(last.err.Error(), "permission denied") {
		t.Fatalf("err = %v, want the first failure kept", last.err)
	}
}

func TestDeleteJobCancelStopsTheWalk(t *testing.T) {
	f := nestedTree()
	f.opDelay = 5 * time.Millisecond

	job, _ := startDeleteJob(f, "/", paneRemote, []domain.Entry{dirEntry("r")}, deleteScan{files: 4, folders: 4}, "r")
	job.cancel()
	last := drainDelete(t, job)

	if last.done == 8 {
		t.Fatal("cancel did not stop the walk")
	}
	if len(f.snapshot()) == 0 {
		t.Fatal("cancel did not stop the walk: the tree was emptied")
	}
}

// The old code put one 60s deadline over the whole recursive delete, so a big
// tree aborted part-way. Every call must now get its own budget instead.
func TestDeleteJobGivesEachStepItsOwnDeadline(t *testing.T) {
	f := nestedTree()
	f.opDelay = 10 * time.Millisecond

	job, _ := startDeleteJob(f, "/", paneRemote, []domain.Entry{dirEntry("r")}, deleteScan{files: 4, folders: 4}, "r")
	drainDelete(t, job)

	_, _, budgets := f.calls()
	if len(budgets) < 8 {
		t.Fatalf("only %d budgets recorded, want one per call", len(budgets))
	}
	// A single walk-wide deadline would shrink with every step. A per-step one
	// is handed out fresh, so even the last call still has almost all of it.
	for i, budget := range budgets {
		if budget < deleteStepTimeout-time.Second {
			t.Fatalf("call %d had %s left of %s — the walk is sharing one deadline", i, budget, deleteStepTimeout)
		}
	}
}

func TestDeleteJobRemovesSymlinkWithoutFollowingIt(t *testing.T) {
	f := newDeleteFS()
	f.put("/", dirEntry("r"), dirEntry("target"))
	f.put("/r", symlinkToDir("link"))
	f.put("/target", file("keep.txt", 1, time.Time{}))

	job, _ := startDeleteJob(f, "/", paneRemote, []domain.Entry{dirEntry("r")}, deleteScan{files: 1, folders: 1}, "r")
	last := drainDelete(t, job)

	if last.failed != 0 {
		t.Fatalf("failed = %d, want 0 (%v)", last.failed, last.err)
	}
	listed, removed, _ := f.calls()
	if indexOf(removed, "/r/link") < 0 {
		t.Fatalf("the link itself was not removed: %v", removed)
	}
	if indexOf(listed, "/target") >= 0 {
		t.Fatalf("the walk followed the link into %v", listed)
	}
	if indexOf(f.snapshot(), "/target/keep.txt") < 0 {
		t.Fatalf("the link's target was deleted through it: %v", f.snapshot())
	}
}

func TestDeleteScanCountsTheTree(t *testing.T) {
	f := nestedTree()
	msg := beginDeleteScan(f, "/", paneRemote, []domain.Entry{dirEntry("r")})().(deleteScanMsg)

	if msg.scan.truncated {
		t.Fatal("scan truncated unexpectedly")
	}
	if msg.scan.files != 4 || msg.scan.folders != 4 {
		t.Fatalf("scan = %d files / %d folders, want 4/4", msg.scan.files, msg.scan.folders)
	}
	if msg.scan.bytes != 100 {
		t.Fatalf("scan bytes = %d, want 100", msg.scan.bytes)
	}
}

func TestDeleteScanTruncatesPastTheCap(t *testing.T) {
	f := newDeleteFS()
	entries := make([]domain.Entry, deleteScanCap+10)
	for i := range entries {
		entries[i] = file(fmt.Sprintf("f%06d", i), 1, time.Time{})
	}
	f.put("/", dirEntry("r"))
	f.put("/r", entries...)

	msg := beginDeleteScan(f, "/", paneRemote, []domain.Entry{dirEntry("r")})().(deleteScanMsg)
	if !msg.scan.truncated {
		t.Fatal("scan past the cap was not marked truncated")
	}
}

// A directory that will not list is the delete's problem to report, not the
// count's: a scan that failed here would put the user back in front of a dead
// prompt instead of a confirmation.
func TestDeleteScanSkipsAnUnreadableDirectory(t *testing.T) {
	f := nestedTree()
	f.failList["/r/a"] = errors.New("permission denied")

	msg := beginDeleteScan(f, "/", paneRemote, []domain.Entry{dirEntry("r")})().(deleteScanMsg)
	if msg.scan.files != 2 {
		t.Fatalf("files = %d, want 2 (a/'s two are unreadable)", msg.scan.files)
	}
	if msg.scan.folders != 4 {
		t.Fatalf("folders = %d, want 4", msg.scan.folders)
	}
}

// deleteModel is a connected model whose remote pane lists f's tree at root.
func deleteModel(t *testing.T, f *deleteFS) Model {
	t.Helper()
	model := loadedModelOver(t, f, f, newScriptedEngine())
	model.focus = focusRemote
	model.remote.path = "/"
	entries, err := f.List(context.Background(), "/", true)
	if err != nil {
		t.Fatal(err)
	}
	model.remote.entries = entries
	model.remote.allEntries = entries
	model.remote.cursor = 0
	return model
}

func TestDeletePromptCountsTheFolderBeforeConfirming(t *testing.T) {
	f := nestedTree()
	model := deleteModel(t, f)

	model = press(t, model, tea.KeyMsg{Type: tea.KeyDelete})

	if model.overlay != overlayFileAction {
		t.Fatalf("overlay = %v, want the delete confirmation", model.overlay)
	}
	if model.fileAction == nil || model.fileAction.scan == nil {
		t.Fatal("prompt opened without a count")
	}
	if model.fileAction.scan.files != 4 || model.fileAction.scan.folders != 4 {
		t.Fatalf("scan = %+v, want 4 files / 4 folders", *model.fileAction.scan)
	}
	plain := ansi.Strip(model.View())
	if !strings.Contains(plain, "4 file(s) in 4 folder(s)") {
		t.Fatalf("overlay does not state what is going:\n%s", plain)
	}
}

// A selection with no folder in it needs no walk: the listing already has the
// numbers, and the prompt should not stall behind a scan.
func TestDeletePromptSkipsTheScanForPlainFiles(t *testing.T) {
	f := nestedTree()
	model := deleteModel(t, f)
	model.remote.path = "/r"
	entries, err := f.List(context.Background(), "/r", true)
	if err != nil {
		t.Fatal(err)
	}
	model.remote.entries, model.remote.allEntries = entries, entries
	model.remote.cursor = indexOfEntry(entries, "top.txt")

	model = press(t, model, tea.KeyMsg{Type: tea.KeyDelete})

	if model.fileAction == nil {
		t.Fatal("prompt did not open")
	}
	if model.fileAction.scan != nil {
		t.Fatalf("plain-file delete was scanned anyway: %+v", *model.fileAction.scan)
	}
	if listed, _, _ := f.calls(); indexOf(listed, "/r/a") >= 0 {
		t.Fatalf("plain-file delete walked the tree: %v", listed)
	}
}

func indexOfEntry(entries []domain.Entry, name string) int {
	for i, e := range entries {
		if e.Name == name {
			return i
		}
	}
	return -1
}

func TestDeleteRunsAsAJobAndReportsWhatItRemoved(t *testing.T) {
	f := nestedTree()
	model := deleteModel(t, f)

	model = press(t, model, tea.KeyMsg{Type: tea.KeyDelete}) // opens the prompt
	model = press(t, model, runes("y"))                      // confirms it

	if model.deleteJob != nil {
		t.Fatal("job still running after the walk settled")
	}
	if left := f.snapshot(); len(left) != 0 {
		t.Fatalf("tree not empty, still holds %v", left)
	}
	if !strings.Contains(model.status, "deleted 8 item(s)") {
		t.Fatalf("status = %q, want the count", model.status)
	}
	joined := strings.Join(model.logs, "\n")
	for _, want := range []string{"deleted /r/a/a1.txt", "deleted /r/b/c/deep.txt", "deleted /r"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("log is missing %q:\n%s", want, joined)
		}
	}
}

func TestSecondDeleteRefusedWhileOneIsRunning(t *testing.T) {
	model := deleteModel(t, nestedTree())
	model.deleteJob = &deleteJob{pane: paneRemote, cancel: func() {}}

	if cmd := model.openDeletePrompt(); cmd != nil {
		t.Fatal("a second delete was allowed to start")
	}
	if model.overlay == overlayFileAction {
		t.Fatal("a second delete opened its prompt")
	}
	if !model.statusErr {
		t.Fatalf("status = %q, want an error", model.status)
	}
}

// x has to reach the delete first: it is the more urgent thing to stop, and
// unlike a transfer it has no queue row to aim at.
func TestCancelKeyStopsTheDeleteBeforeTheTransferQueue(t *testing.T) {
	model := deleteModel(t, nestedTree())
	canceled := false
	model.deleteJob = &deleteJob{pane: paneRemote, cancel: func() { canceled = true }}
	model.transfers = []domain.Transfer{{ID: 1, Status: domain.Active}}

	updated, _ := model.updateKey(runes("x"))
	model = updated.(Model)

	if !canceled {
		t.Fatal("x did not cancel the running delete")
	}
	if !model.deleteJob.canceled {
		t.Fatal("job not marked cancelled")
	}
	if model.transfers[0].Status != domain.Active {
		t.Fatal("x cancelled a transfer as well as the delete")
	}
}

// The pinned block takes its rows out of the tab content's, so the renderer
// and the scroll arithmetic have to subtract the same number.
func TestDeleteRowTakesRowsFromTheBottomPane(t *testing.T) {
	model := deleteModel(t, nestedTree())
	before := model.bottomVisibleRows()

	model.deleteJob = &deleteJob{pane: paneRemote, total: 100, done: 32, cancel: func() {}}

	if after := model.bottomVisibleRows(); after != before-deleteRowHeight {
		t.Fatalf("visible rows = %d, want %d", after, before-deleteRowHeight)
	}
	model.focus = focusQueue
	model.bottomTab = tabQueue
	model.transfers = make([]domain.Transfer, 40)
	for i := range model.transfers {
		model.transfers[i] = domain.Transfer{ID: i + 1, Status: domain.Active}
	}
	model.bottomCursor = 39
	model.clampBottomCursor()
	if model.bottomCursor >= model.bottomOffset+model.bottomVisibleRows() {
		t.Fatalf("cursor %d is outside the window at offset %d (%d rows)", model.bottomCursor, model.bottomOffset, model.bottomVisibleRows())
	}
}

// The row is pinned above the tabs rather than living in one of them, so it
// stays visible wherever the user happens to be looking.
func TestDeleteRowShowsOnEveryBottomTab(t *testing.T) {
	model := deleteModel(t, nestedTree())
	model.deleteJob = &deleteJob{pane: paneRemote, total: 3910, done: 1248, current: "/r/a/a1.txt", cancel: func() {}}

	for _, tab := range []bottomTab{tabQueue, tabActive, tabFailed, tabHistory, tabLog, tabStats} {
		model.bottomTab = tab
		plain := ansi.Strip(model.View())
		if !strings.Contains(plain, "Deleting  1248/3910") {
			t.Fatalf("tab %d does not show the delete row:\n%s", tab, plain)
		}
		if !strings.Contains(plain, "/r/a/a1.txt") {
			t.Fatalf("tab %d does not show the current path:\n%s", tab, plain)
		}
	}
}

// A truncated count undercounts, so the bar must not sit at 100% while the
// walk carries on.
func TestDeleteProgressHoldsShortOfFullWhenTheCountWasTruncated(t *testing.T) {
	job := &deleteJob{total: 100, done: 250, truncated: true}
	if got := job.progress(); got != 0.99 {
		t.Fatalf("progress = %v, want 0.99", got)
	}
	if got := job.totalLabel(); got != "100+" {
		t.Fatalf("totalLabel = %q, want \"100+\"", got)
	}
	exact := &deleteJob{total: 100, done: 100}
	if got := exact.progress(); got != 1 {
		t.Fatalf("progress = %v, want 1", got)
	}
}

// The Log tab could be unbounded while only connects and errors wrote to it.
// A delete streams a line per file, which makes the cap load-bearing.
func TestAppendLogTrimsToTheCap(t *testing.T) {
	model := Model{}
	for i := 0; i < maxLogLines+250; i++ {
		model.appendLog(fmt.Sprintf("line %d", i))
	}
	if len(model.logs) != maxLogLines {
		t.Fatalf("logs = %d lines, want %d", len(model.logs), maxLogLines)
	}
	if model.logs[0] != "line 250" {
		t.Fatalf("oldest line = %q, want \"line 250\"", model.logs[0])
	}
	if model.logs[len(model.logs)-1] != fmt.Sprintf("line %d", maxLogLines+249) {
		t.Fatalf("newest line = %q", model.logs[len(model.logs)-1])
	}
}

// A fast local delete rounded to the second reported "0s", which reads as
// though nothing happened.
func TestDeleteElapsedKeepsSubSecondWorkVisible(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{412 * time.Millisecond, "412ms"},
		{2 * time.Millisecond, "2ms"},
		{90 * time.Second, "1m30s"},
	} {
		if got := deleteElapsed(tc.in); got != tc.want {
			t.Errorf("deleteElapsed(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A truncated count marks the whole phrase once rather than tagging each
// number, since a "+" on a folder count that happened to be exact read as
// noise.
func TestDeleteScanCountsStateTheBoundOnce(t *testing.T) {
	exact := deleteScanCounts(deleteScan{files: 4, folders: 2, bytes: 2048})
	if exact != "4 file(s) in 2 folder(s) — 2.0 KB" {
		t.Fatalf("exact = %q", exact)
	}
	short := deleteScanCounts(deleteScan{files: 19940, folders: 60, truncated: true})
	if short != "at least 19940 file(s) in 60 folder(s)" {
		t.Fatalf("truncated = %q", short)
	}
}
