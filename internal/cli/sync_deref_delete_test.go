package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"tideftp/internal/localfs"
)

// With --dereference --delete, a symlinked directory at the destination that
// the source does not have must be removed as a link: nothing behind it —
// which lives outside the tree being synced — may be deleted.
func TestSyncDeleteNeverReachesThroughADestinationLink(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "precious.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dst, "link")); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	fsys := localfs.New()
	r := &syncRun{del: true, dereference: true}
	srcAll, err := walkTree(ctx, fsys, src, r.filter, true, false)
	if err != nil {
		t.Fatal(err)
	}
	dstAll, err := walkTree(ctx, fsys, dst, r.filter, true, false)
	if err != nil {
		t.Fatal(err)
	}
	ops, _, err := r.plan(ctx, srcAll, dstAll)
	if err != nil {
		t.Fatal(err)
	}

	removesLink := false
	for _, op := range ops {
		switch {
		case op.rel == "link/precious.txt":
			t.Fatalf("plan deletes %q, a file behind a destination symlink", op.rel)
		case op.rel == "link" && op.kind == opDelDir:
			removesLink = true
		}
	}
	if !removesLink {
		t.Fatalf("plan = %+v, want the orphaned link itself removed", ops)
	}
}
