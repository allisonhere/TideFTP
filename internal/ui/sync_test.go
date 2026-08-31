package ui

import (
	"context"
	"io"
	"io/fs"
	"path"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
	"tideftp/internal/vfs"
)

// syncFS is an in-memory vfs.FS for the mirror tests: a directory→entries
// map that List reads and Remove mutates, so a prune's effect can be
// asserted. Paths are POSIX and cleaned.
type syncFS struct {
	tree map[string][]domain.Entry
}

var _ vfs.FS = (*syncFS)(nil)

func newSyncFS() *syncFS { return &syncFS{tree: map[string][]domain.Entry{}} }

func syncClean(p string) string {
	if strings.TrimSpace(p) == "" {
		return "/"
	}
	return path.Clean("/" + strings.TrimPrefix(p, "/"))
}

// put registers dir with the given children, and makes sure each child dir
// has its own (possibly empty) entry so a walk can descend into it.
func (f *syncFS) put(dir string, entries ...domain.Entry) {
	dir = syncClean(dir)
	f.tree[dir] = entries
	for _, e := range entries {
		if e.IsDir() {
			child := syncClean(path.Join(dir, e.Name))
			if _, ok := f.tree[child]; !ok {
				f.tree[child] = nil
			}
		}
	}
}

func (f *syncFS) List(_ context.Context, dir string, showHidden bool) ([]domain.Entry, error) {
	entries, ok := f.tree[syncClean(dir)]
	if !ok {
		return nil, &fsMissingDirError{dir}
	}
	if showHidden {
		return entries, nil
	}
	out := entries[:0:0]
	for _, e := range entries {
		if !strings.HasPrefix(e.Name, ".") {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *syncFS) Child(current, name string) string {
	return syncClean(path.Join(syncClean(current), name))
}
func (f *syncFS) Parent(current string) string {
	p := path.Dir(syncClean(current))
	if p == "." {
		return "/"
	}
	return p
}

func (f *syncFS) Remove(_ context.Context, target string) error {
	target = syncClean(target)
	parent := f.Parent(target)
	base := path.Base(target)
	kids := f.tree[parent]
	for i, e := range kids {
		if e.Name == base {
			f.tree[parent] = append(append([]domain.Entry(nil), kids[:i]...), kids[i+1:]...)
			delete(f.tree, target)
			return nil
		}
	}
	return &fsMissingDirError{target}
}

func (f *syncFS) Mkdir(_ context.Context, dir string) error        { f.tree[syncClean(dir)] = nil; return nil }
func (f *syncFS) Rename(context.Context, string, string) error     { return nil }
func (f *syncFS) Chmod(context.Context, string, fs.FileMode) error { return nil }
func (f *syncFS) ReadFile(context.Context, string) ([]byte, error) { return nil, nil }
func (f *syncFS) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (f *syncFS) WriteFile(context.Context, string, []byte) error { return nil }

type fsMissingDirError struct{ dir string }

func (e *fsMissingDirError) Error() string { return "no such directory: " + e.dir }

func file(name string, size int64, mod time.Time) domain.Entry {
	return domain.Entry{Name: name, Kind: domain.EntryFile, Size: size, Mode: "-rw-r--r--", Modified: mod}
}
func dirEntry(name string) domain.Entry {
	return domain.Entry{Name: name, Kind: domain.EntryDir, Mode: "drwxr-xr-x"}
}

// mirrorModel wires a connected model whose local pane is at /src over srcFS
// and whose remote pane is at /dst over dstFS, focused on the local pane.
func mirrorModel(t *testing.T, srcFS, dstFS *syncFS) (Model, *scriptedEngine) {
	t.Helper()
	engine := newScriptedEngine()
	model := loadedModelOver(t, srcFS, dstFS, engine)
	model.focus = focusLocal
	model.local.path = "/src"
	model.remote.path = "/dst"
	model.local.entries = nil
	model.remote.entries = nil
	return model, engine
}

func TestSyncNeedsCopy(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)

	if need, _ := syncNeedsCopy(file("a", 10, old), domain.Entry{}, false); !need {
		t.Fatal("a missing destination must be a copy")
	}
	if need, upd := syncNeedsCopy(file("a", 20, old), file("a", 10, old), true); !need || !upd {
		t.Fatal("a size difference must be an update")
	}
	if need, upd := syncNeedsCopy(file("a", 10, newer), file("a", 10, old), true); !need || !upd {
		t.Fatal("a newer source must be an update")
	}
	if need, _ := syncNeedsCopy(file("a", 10, old), file("a", 10, newer), true); need {
		t.Fatal("an older, same-size source must be left alone")
	}
	if need, _ := syncNeedsCopy(file("a", 10, old.Add(time.Second)), file("a", 10, old), true); need {
		t.Fatal("a sub-skew mtime bump must not count as a change")
	}
}

func TestSyncScanClassifiesFiles(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	src := newSyncFS()
	src.put("/src",
		file("new.txt", 100, old),
		file("changed.txt", 200, newer),
		file("same.txt", 50, old),
	)
	dst := newSyncFS()
	dst.put("/dst",
		file("changed.txt", 111, old),
		file("same.txt", 50, old),
	)
	model, _ := mirrorModel(t, src, dst)

	model = press(t, model, runes("M"))
	if model.overlay != overlaySync || model.sync == nil {
		t.Fatalf("'M' did not open the mirror overlay (overlay=%v)", model.overlay)
	}
	p := model.sync
	if p.newCount() != 1 || p.updateCount() != 1 || p.identical != 1 {
		t.Fatalf("plan new=%d updated=%d identical=%d, want 1/1/1", p.newCount(), p.updateCount(), p.identical)
	}
	if p.totalBytes != 300 {
		t.Fatalf("totalBytes = %d, want 300 (new 100 + changed 200)", p.totalBytes)
	}
}

func TestSyncConfirmQueuesCopiesOnly(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := newSyncFS()
	src.put("/src", file("new.txt", 100, old), file("same.txt", 50, old))
	dst := newSyncFS()
	dst.put("/dst", file("same.txt", 50, old))
	model, engine := mirrorModel(t, src, dst)

	model = press(t, model, runes("M"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if model.overlay != overlayNone {
		t.Fatalf("overlay still open after confirm: %v", model.overlay)
	}
	if len(model.transfers) != 1 {
		t.Fatalf("queued %d transfers, want 1 (new.txt only)", len(model.transfers))
	}
	if got := model.transfers[0].Source; got != "/src/new.txt" {
		t.Fatalf("queued source = %q, want /src/new.txt", got)
	}
	if model.transfers[0].Direction != domain.Upload {
		t.Fatalf("direction = %v, want Upload (local pane focused)", model.transfers[0].Direction)
	}
	if len(engine.started) != 1 {
		t.Fatalf("engine started %d, want 1", len(engine.started))
	}
}

func TestSyncPruneOffByDefault(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := newSyncFS()
	src.put("/src", file("keep.txt", 10, old))
	dst := newSyncFS()
	dst.put("/dst", file("keep.txt", 10, old), file("extra.txt", 999, old))
	model, _ := mirrorModel(t, src, dst)

	model = press(t, model, runes("M"))
	if model.sync == nil || model.sync.pruneFileCount() != 1 {
		t.Fatalf("plan did not spot the extra file")
	}
	if model.sync.prune {
		t.Fatalf("prune must start disarmed")
	}
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if _, err := dst.List(context.Background(), "/dst", true); err != nil {
		t.Fatalf("dst list: %v", err)
	}
	kids, _ := dst.List(context.Background(), "/dst", true)
	if !hasEntry(kids, "extra.txt") {
		t.Fatalf("extra.txt was removed even though prune was off")
	}
}

func TestSyncPruneArmedDeletesExtras(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := newSyncFS()
	src.put("/src", file("keep.txt", 10, old))
	dst := newSyncFS()
	dst.put("/dst", file("keep.txt", 10, old), file("extra.txt", 999, old), dirEntry("stale"))
	dst.put("/dst/stale", file("old.log", 5, old))
	model, _ := mirrorModel(t, src, dst)

	model = press(t, model, runes("M"))
	model = press(t, model, runes("p")) // arm prune
	if !model.sync.prune {
		t.Fatalf("'p' did not arm prune")
	}
	// extra.txt + stale/ + stale/old.log
	if model.sync.pruneFileCount() != 2 || model.sync.pruneDirCount() != 1 {
		t.Fatalf("prune plan files=%d dirs=%d, want 2/1", model.sync.pruneFileCount(), model.sync.pruneDirCount())
	}
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	kids, _ := dst.List(context.Background(), "/dst", true)
	if hasEntry(kids, "extra.txt") || hasEntry(kids, "stale") {
		t.Fatalf("prune left extras behind: %v", entryNames(kids))
	}
	if _, err := dst.List(context.Background(), "/dst/stale", true); err == nil {
		t.Fatalf("pruned directory /dst/stale still lists")
	}
}

func TestSyncAlreadyInSync(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := newSyncFS()
	src.put("/src", file("a.txt", 10, old))
	dst := newSyncFS()
	dst.put("/dst", file("a.txt", 10, old))
	model, _ := mirrorModel(t, src, dst)

	model = press(t, model, runes("M"))
	if model.overlay != overlayNone {
		t.Fatalf("identical trees opened an overlay: %v", model.overlay)
	}
	if !strings.Contains(model.status, "sync") {
		t.Fatalf("status = %q, want it to mention being in sync", model.status)
	}
}

func TestSyncScopesToDirectoryUnderCursor(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := newSyncFS()
	src.put("/src", dirEntry("assets"), file("top.txt", 10, old))
	src.put("/src/assets", file("logo.png", 40, old))
	dst := newSyncFS()
	dst.put("/dst")
	model, _ := mirrorModel(t, src, dst)
	model.local.entries = []domain.Entry{dirEntry("assets"), file("top.txt", 10, old)}
	model.local.cursor = 0 // on "assets"

	model = press(t, model, runes("M"))
	if model.sync == nil {
		t.Fatal("no plan")
	}
	if model.sync.srcRoot != "/src/assets" {
		t.Fatalf("srcRoot = %q, want /src/assets (scoped to the highlighted dir)", model.sync.srcRoot)
	}
	if model.sync.newCount() != 1 {
		t.Fatalf("newCount = %d, want just assets/logo.png", model.sync.newCount())
	}
}

func TestSyncNeedsConnection(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model = settle(t, model, func() tea.Msg { return disconnectedMsg{conn: model.conn} })
	model.focus = focusLocal

	model = press(t, model, runes("M"))
	if model.overlay == overlaySync {
		t.Fatalf("mirror ran without a connection")
	}
	if !model.statusErr {
		t.Fatalf("mirror without a connection did not report an error")
	}
}

func hasEntry(entries []domain.Entry, name string) bool {
	for _, e := range entries {
		if e.Name == name {
			return true
		}
	}
	return false
}

func entryNames(entries []domain.Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name
	}
	return out
}
