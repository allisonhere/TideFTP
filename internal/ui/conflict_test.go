package ui

import (
	"context"
	"path"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
	"tideftp/internal/vfs"
)

// conflictFS is a minimal vfs.FS test double whose List result at each
// directory is exactly what a test seeds — full control for exercising
// destination-conflict detection, which fakefs's fixed tree can't offer.
type conflictFS struct {
	entries map[string][]domain.Entry
}

var _ vfs.FS = (*conflictFS)(nil)

func (f *conflictFS) List(_ context.Context, dir string, _ bool) ([]domain.Entry, error) {
	return f.entries[dir], nil
}
func (f *conflictFS) Child(current, name string) string                { return path.Join(current, name) }
func (f *conflictFS) Parent(current string) string                     { return path.Dir(current) }
func (f *conflictFS) Mkdir(context.Context, string) error              { return nil }
func (f *conflictFS) Rename(context.Context, string, string) error     { return nil }
func (f *conflictFS) Remove(context.Context, string) error             { return nil }
func (f *conflictFS) ReadFile(context.Context, string) ([]byte, error) { return nil, nil }
func (f *conflictFS) WriteFile(context.Context, string, []byte) error  { return nil }

// conflictModel builds a connected model uploading from a single local file
// entry, with dst seeded so the remote directory the upload targets (the
// target's StartPath, testTarget.StartPath) lists whatever conflicts is set
// to.
func conflictModel(t *testing.T, source domain.Entry, conflicts []domain.Entry) Model {
	t.Helper()
	dst := &conflictFS{entries: map[string][]domain.Entry{
		testTarget.StartPath: conflicts,
	}}
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: dst, engine: newScriptedEngine()})
	model.focus = focusLocal
	model.local.entries = []domain.Entry{source}
	model.local.cursor = 0
	return model
}

func TestQueueingWithNoConflictQueuesInstantly(t *testing.T) {
	model := conflictModel(t, domain.Entry{Name: "fresh.txt", Size: 10}, nil)

	model = press(t, model, runes("u"))

	if model.overlay != overlayNone {
		t.Fatalf("overlay = %v, want overlayNone: an uncontested queue must stay instant", model.overlay)
	}
	if len(model.transfers) != 1 {
		t.Fatalf("transfers = %+v, want exactly one queued", model.transfers)
	}
}

func TestQueueingAConflictingFileOpensTheOverlay(t *testing.T) {
	model := conflictModel(t, domain.Entry{Name: "existing.txt", Size: 20},
		[]domain.Entry{{Name: "existing.txt", Size: 10}})

	model = press(t, model, runes("u"))

	if model.overlay != overlayConflict {
		t.Fatalf("overlay = %v, want overlayConflict", model.overlay)
	}
	if model.preflight == nil || !model.preflight.hasConflicts() || model.preflight.conflictCount() != 1 {
		t.Fatalf("preflight = %+v, want one detected conflict", model.preflight)
	}
	if len(model.transfers) != 0 {
		t.Fatalf("nothing should be queued before the conflict is resolved: %+v", model.transfers)
	}
}

// resolveConflict opens the overlay for source/conflicts, points its cursor
// at policy, and confirms with enter (queue scope) or s (session scope).
func resolveConflict(t *testing.T, source domain.Entry, conflicts []domain.Entry, policy conflictPolicy, remember bool) Model {
	t.Helper()
	model := conflictModel(t, source, conflicts)
	model = press(t, model, runes("u"))
	if model.overlay != overlayConflict {
		t.Fatalf("setup: overlay = %v, want overlayConflict", model.overlay)
	}
	model.preflight.cursor = int(policy)
	key := tea.KeyMsg{Type: tea.KeyEnter}
	if remember {
		key = runes("s")
	}
	return press(t, model, key)
}

func TestConflictOverwriteQueuesUnconditionally(t *testing.T) {
	model := resolveConflict(t, domain.Entry{Name: "a.txt", Size: 20}, []domain.Entry{{Name: "a.txt", Size: 999}}, conflictOverwrite, false)

	if model.overlay != overlayNone || model.preflight != nil {
		t.Fatalf("overlay/preflight did not clear: overlay=%v preflight=%v", model.overlay, model.preflight)
	}
	if len(model.transfers) != 1 || model.transfers[0].ResumeFrom != 0 {
		t.Fatalf("transfers = %+v, want one plain overwrite", model.transfers)
	}
}

func TestConflictOverwriteIfSourceNewerAppliesPerFileWhenAppliedToAll(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(24 * time.Hour)

	dst := &conflictFS{entries: map[string][]domain.Entry{
		testTarget.StartPath: {
			{Name: "newer.txt", Size: 5, Modified: old},
			{Name: "older.txt", Size: 5, Modified: newer},
		},
	}}
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: dst, engine: newScriptedEngine()})
	model.focus = focusLocal
	model.local.entries = []domain.Entry{
		{Name: "newer.txt", Size: 5, Modified: newer}, // source is newer: should queue
		{Name: "older.txt", Size: 5, Modified: old},   // source is not newer: should skip
	}
	model.local.cursor = 0
	model.local.selected = map[string]bool{"newer.txt": true, "older.txt": true}

	model = press(t, model, runes("u"))
	if model.overlay != overlayConflict {
		t.Fatalf("overlay = %v, want overlayConflict", model.overlay)
	}
	model.preflight.cursor = int(conflictOverwriteIfSourceNewer)
	model = press(t, model, runes("a"))

	if len(model.transfers) != 1 || model.transfers[0].Destination != testTarget.StartPath+"/newer.txt" {
		t.Fatalf("transfers = %+v, want only newer.txt queued", model.transfers)
	}
}

func TestConflictOverwriteIfDifferentSizeAppliesPerFileWhenAppliedToAll(t *testing.T) {
	dst := &conflictFS{entries: map[string][]domain.Entry{
		testTarget.StartPath: {
			{Name: "changed.txt", Size: 5},
			{Name: "same.txt", Size: 10},
		},
	}}
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: dst, engine: newScriptedEngine()})
	model.focus = focusLocal
	model.local.entries = []domain.Entry{
		{Name: "changed.txt", Size: 20}, // different size: should queue
		{Name: "same.txt", Size: 10},    // same size: should skip
	}
	model.local.cursor = 0
	model.local.selected = map[string]bool{"changed.txt": true, "same.txt": true}

	model = press(t, model, runes("u"))
	model.preflight.cursor = int(conflictOverwriteIfDifferentSize)
	model = press(t, model, runes("a"))

	if len(model.transfers) != 1 || model.transfers[0].Destination != testTarget.StartPath+"/changed.txt" {
		t.Fatalf("transfers = %+v, want only changed.txt queued", model.transfers)
	}
}

func TestConflictResumeSetsOffsetAndSkipsCompleteFiles(t *testing.T) {
	dst := &conflictFS{entries: map[string][]domain.Entry{
		testTarget.StartPath: {
			{Name: "partial.bin", Size: 40},   // half-uploaded: should resume
			{Name: "complete.bin", Size: 100}, // already >= source: nothing to resume
		},
	}}
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: dst, engine: newScriptedEngine()})
	model.focus = focusLocal
	model.local.entries = []domain.Entry{
		{Name: "partial.bin", Size: 100},
		{Name: "complete.bin", Size: 100},
	}
	model.local.cursor = 0
	model.local.selected = map[string]bool{"partial.bin": true, "complete.bin": true}

	model = press(t, model, runes("u"))
	model.preflight.cursor = int(conflictResume)
	model = press(t, model, runes("a"))

	if len(model.transfers) != 1 {
		t.Fatalf("transfers = %+v, want only partial.bin queued (complete.bin has nothing to resume)", model.transfers)
	}
	tr := model.transfers[0]
	if tr.Destination != testTarget.StartPath+"/partial.bin" || tr.ResumeFrom != 40 || tr.BytesDone != 40 {
		t.Fatalf("transfer = %+v, want partial.bin resuming from byte 40", tr)
	}
}

func TestConflictRenameAvoidsCollidingWithASibling(t *testing.T) {
	dst := &conflictFS{entries: map[string][]domain.Entry{
		testTarget.StartPath: {
			{Name: "photo.jpg", Size: 999},
			{Name: "photo (1).jpg", Size: 999}, // pre-existing: candidate 1 must be skipped
		},
	}}
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: dst, engine: newScriptedEngine()})
	model.focus = focusLocal
	model.local.entries = []domain.Entry{{Name: "photo.jpg", Size: 20}}
	model.local.cursor = 0

	model = press(t, model, runes("u"))
	model.preflight.cursor = int(conflictRename)
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	want := testTarget.StartPath + "/photo (2).jpg"
	if len(model.transfers) != 1 || model.transfers[0].Destination != want {
		t.Fatalf("transfers = %+v, want the renamed destination %q", model.transfers, want)
	}
}

func TestConflictSkipQueuesNothing(t *testing.T) {
	model := resolveConflict(t, domain.Entry{Name: "a.txt", Size: 20}, []domain.Entry{{Name: "a.txt", Size: 5}}, conflictSkip, false)

	if len(model.transfers) != 0 {
		t.Fatalf("transfers = %+v, want nothing queued after Skip", model.transfers)
	}
}

func TestConflictSessionScopeRemembersThePolicyForLaterBatches(t *testing.T) {
	model := resolveConflict(t, domain.Entry{Name: "a.txt", Size: 20}, []domain.Entry{{Name: "a.txt", Size: 5}}, conflictSkip, true)

	if model.sessionConflictPolicy == nil || *model.sessionConflictPolicy != conflictSkip {
		t.Fatalf("sessionConflictPolicy = %v, want conflictSkip remembered", model.sessionConflictPolicy)
	}

	// A second, unrelated conflicting batch must resolve automatically —
	// same remembered policy, no overlay.
	dstFS := model.remoteFS.(*conflictFS)
	dstFS.entries[testTarget.StartPath] = append(dstFS.entries[testTarget.StartPath], domain.Entry{Name: "b.txt", Size: 5})
	model.local.entries = []domain.Entry{{Name: "b.txt", Size: 20}}
	model.local.selected = nil
	model.local.cursor = 0

	model = press(t, model, runes("u"))

	if model.overlay != overlayNone {
		t.Fatalf("overlay = %v, want overlayNone: a remembered policy must not prompt again", model.overlay)
	}
	if len(model.transfers) != 0 {
		t.Fatalf("transfers = %+v, want nothing queued (remembered policy was Skip)", model.transfers)
	}
}

func TestConflictThisFileAdvancesToTheNextConflictWithoutQueuing(t *testing.T) {
	dst := &conflictFS{entries: map[string][]domain.Entry{
		testTarget.StartPath: {
			{Name: "a.txt", Size: 5},
			{Name: "b.txt", Size: 5},
		},
	}}
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: dst, engine: newScriptedEngine()})
	model.focus = focusLocal
	model.local.entries = []domain.Entry{
		{Name: "a.txt", Size: 20},
		{Name: "b.txt", Size: 20},
	}
	model.local.cursor = 0
	model.local.selected = map[string]bool{"a.txt": true, "b.txt": true}

	model = press(t, model, runes("u"))
	if model.overlay != overlayConflict || model.preflight.conflictCount() != 2 {
		t.Fatalf("setup: overlay=%v conflicts=%v", model.overlay, model.preflight)
	}

	model.preflight.cursor = int(conflictOverwrite)
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if model.overlay != overlayConflict || model.preflight == nil {
		t.Fatalf("resolving the first of two conflicts closed the overlay early: overlay=%v", model.overlay)
	}
	if len(model.transfers) != 0 {
		t.Fatalf("transfers = %+v, want nothing queued until every conflict in the batch is resolved", model.transfers)
	}
	idx := model.preflight.currentConflictIndex()
	if idx < 0 || model.preflight.files[idx].name != "b.txt" {
		t.Fatalf("expected the overlay to now be asking about b.txt, files = %+v", model.preflight.files)
	}
}

func TestConflictThisFileResolvesEachFileWithItsOwnPolicy(t *testing.T) {
	dst := &conflictFS{entries: map[string][]domain.Entry{
		testTarget.StartPath: {
			{Name: "keep.txt", Size: 5},
			{Name: "drop.txt", Size: 5},
		},
	}}
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: dst, engine: newScriptedEngine()})
	model.focus = focusLocal
	model.local.entries = []domain.Entry{
		{Name: "keep.txt", Size: 20},
		{Name: "drop.txt", Size: 20},
	}
	model.local.cursor = 0
	model.local.selected = map[string]bool{"keep.txt": true, "drop.txt": true}

	model = press(t, model, runes("u"))
	model.preflight.cursor = int(conflictOverwrite)
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // resolves keep.txt: Overwrite

	if model.overlay != overlayConflict {
		t.Fatalf("overlay = %v, want it still open on drop.txt", model.overlay)
	}
	model.preflight.cursor = int(conflictSkip)
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // resolves drop.txt: Skip

	if model.overlay != overlayNone || model.preflight != nil {
		t.Fatalf("overlay/preflight did not clear once every conflict was resolved: overlay=%v preflight=%v", model.overlay, model.preflight)
	}
	if len(model.transfers) != 1 || model.transfers[0].Destination != testTarget.StartPath+"/keep.txt" {
		t.Fatalf("transfers = %+v, want only keep.txt queued (drop.txt was Skipped)", model.transfers)
	}
}

func TestConflictCancelDropsTheWholeBatch(t *testing.T) {
	dst := &conflictFS{entries: map[string][]domain.Entry{
		testTarget.StartPath: {{Name: "conflict.txt", Size: 5}},
	}}
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: dst, engine: newScriptedEngine()})
	model.focus = focusLocal
	model.local.entries = []domain.Entry{
		{Name: "conflict.txt", Size: 20},
		{Name: "clean.txt", Size: 20},
	}
	model.local.cursor = 0
	model.local.selected = map[string]bool{"conflict.txt": true, "clean.txt": true}

	model = press(t, model, runes("u"))
	if model.overlay != overlayConflict {
		t.Fatalf("overlay = %v, want overlayConflict", model.overlay)
	}

	model = press(t, model, tea.KeyMsg{Type: tea.KeyEsc})

	if model.overlay != overlayNone || model.preflight != nil {
		t.Fatalf("overlay/preflight did not clear on cancel: overlay=%v preflight=%v", model.overlay, model.preflight)
	}
	if len(model.transfers) != 0 {
		t.Fatalf("transfers = %+v, want nothing queued — cancel drops clean files too", model.transfers)
	}
}
