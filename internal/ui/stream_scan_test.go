package ui

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"tideftp/internal/domain"
	"tideftp/internal/vfs"
)

// flakyListFS fails every List of one directory.
type flakyListFS struct {
	vfs.FS
	bad   string
	calls int
}

func (f *flakyListFS) List(ctx context.Context, dir string, hidden bool) ([]domain.Entry, error) {
	if dir == f.bad {
		f.calls++
		return nil, errors.New("421 too many connections")
	}
	return f.FS.List(ctx, dir, hidden)
}

func TestStreamingScanBatchesPerDirectoryAndSkipsBadFolder(t *testing.T) {
	src, dst := newSyncFS(), newSyncFS()
	src.put("/src", dirEntry("a"), dirEntry("b"))
	src.put("/src/a", file("1.txt", 1, time.Time{}), file("2.txt", 1, time.Time{}))
	src.put("/src/b", file("3.txt", 1, time.Time{}))
	dst.put("/dst")
	bad := &flakyListFS{FS: src, bad: "/src/b"}

	var batches []preflightScan
	entries := []domain.Entry{dirEntry("a"), dirEntry("b")}
	msg := runPreflightScan(domain.Download, entries, "/src", "/dst", bad, dst, true, math.MaxInt, 1, nil,
		func(b preflightScan) { batches = append(batches, b) }, context.Background(), 0)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	if len(batches) != 1 || len(batches[0].files) != 2 {
		t.Fatalf("batches = %+v, want one batch with the two files from /src/a", batches)
	}
	if msg.scan.streamed != 2 || len(msg.scan.files) != 0 {
		t.Fatalf("streamed=%d leftover=%d, want 2/0", msg.scan.streamed, len(msg.scan.files))
	}
	if msg.scan.failedDirs != 1 || bad.calls != scanListAttempts {
		t.Fatalf("failedDirs=%d calls=%d, want 1 failure after %d attempts", msg.scan.failedDirs, bad.calls, scanListAttempts)
	}
}

func TestBufferedScanSkipsBadFolderInsteadOfAborting(t *testing.T) {
	src, dst := newSyncFS(), newSyncFS()
	src.put("/src", dirEntry("a"), dirEntry("b"))
	src.put("/src/a", file("1.txt", 1, time.Time{}))
	dst.put("/dst")
	bad := &flakyListFS{FS: src, bad: "/src/b"}

	msg := runPreflightScan(domain.Download, []domain.Entry{dirEntry("a"), dirEntry("b")}, "/src", "/dst", bad, dst, true, math.MaxInt, 1, nil, nil, context.Background(), 0)
	if msg.err != nil {
		t.Fatalf("err = %v, want the scan to survive one unlistable folder", msg.err)
	}
	if len(msg.scan.files) != 1 || msg.scan.failedDirs != 1 {
		t.Fatalf("files=%d failedDirs=%d, want 1/1", len(msg.scan.files), msg.scan.failedDirs)
	}
}

func TestConflictPolicyKeysRoundTrip(t *testing.T) {
	for p := conflictPolicy(0); p < conflictPolicyCount; p++ {
		got := parseConflictPolicy(conflictPolicyKey(p))
		if got == nil || *got != p {
			t.Errorf("policy %d did not round-trip: %v", p, got)
		}
	}
	if parseConflictPolicy("") != nil || parseConflictPolicy("bogus") != nil {
		t.Error("empty/unknown must mean ask")
	}
}
