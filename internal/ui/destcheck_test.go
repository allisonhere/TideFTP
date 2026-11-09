package ui

import (
	"context"
	"errors"
	"testing"

	"tideftp/internal/domain"
)

func fileEntry(name string) domain.Entry {
	return domain.Entry{Name: name, Kind: domain.EntryFile, Size: 1}
}

// A destination folder that exists but will not list must not be treated as
// empty: its files would be queued as new and overwrite what is there,
// whatever "If file exists" says. They are held back and reported instead.
func TestCheckDestinationsHoldsBackFilesWhoseFolderWillNotList(t *testing.T) {
	dst := newDeleteFS().put("/", dirEntry("dst")).put("/dst", fileEntry("a.txt"))
	dst.failList["/dst"] = errors.New("connection reset")
	scan := preflightScan{dstFS: dst, files: []preflightFile{
		{src: "/src/a.txt", dst: "/dst/a.txt", name: "a.txt", size: 1},
	}, totalBytes: 1}

	checkDestinations(context.Background(), &scan)

	if len(scan.files) != 0 {
		t.Fatalf("files = %+v, want a.txt held back rather than queued without a conflict check", scan.files)
	}
	if scan.failedDirs != 1 || scan.firstFailure == "" {
		t.Fatalf("failedDirs = %d (%q), want the unlistable destination reported", scan.failedDirs, scan.firstFailure)
	}
}

// A destination folder that simply does not exist yet — however deep — has
// nothing in it to conflict with, and is not a failure.
func TestCheckDestinationsTreatsAMissingFolderAsEmpty(t *testing.T) {
	dst := newDeleteFS().put("/", dirEntry("dst")).put("/dst", fileEntry("a.txt"))
	scan := preflightScan{dstFS: dst, files: []preflightFile{
		{src: "/src/a.txt", dst: "/dst/a.txt", name: "a.txt", size: 1},
		{src: "/src/new/deeper/b.txt", dst: "/dst/new/deeper/b.txt", name: "b.txt", size: 1},
	}}

	checkDestinations(context.Background(), &scan)

	if scan.failedDirs != 0 {
		t.Fatalf("failedDirs = %d (%s), want none for a folder that is just not there yet", scan.failedDirs, scan.firstFailure)
	}
	if len(scan.files) != 2 || scan.files[0].conflict == nil || scan.files[1].conflict != nil {
		t.Fatalf("files = %+v, want a.txt conflicting and b.txt new", scan.files)
	}
}

// The mirror scan fails rather than planning an unconditional overwrite of
// every file in a destination folder it could not read.
func TestMirrorScanFailsWhenADestinationFolderWillNotList(t *testing.T) {
	src := newDeleteFS().put("/", dirEntry("src")).put("/src", dirEntry("sub")).put("/src/sub", fileEntry("f.txt"))
	dst := newDeleteFS().put("/", dirEntry("dst")).put("/dst", dirEntry("sub")).put("/dst/sub", fileEntry("f.txt"))
	dst.failList["/dst/sub"] = errors.New("connection reset")

	m := Model{scanFileLimit: defaultScanFileLimit}
	msg := m.beginSyncScan(domain.Upload, "/src", "/dst", src, dst, true)().(syncScanMsg)

	if msg.err == nil {
		t.Fatalf("mirror scan succeeded with plan %+v, want it to fail on the unreadable destination", msg.plan.copies)
	}
}

// A destination root that does not exist yet is still fine to mirror into.
func TestMirrorScanIntoAMissingDestinationRoot(t *testing.T) {
	src := newDeleteFS().put("/", dirEntry("src")).put("/src", fileEntry("f.txt"))
	dst := newDeleteFS().put("/")

	m := Model{scanFileLimit: defaultScanFileLimit}
	msg := m.beginSyncScan(domain.Upload, "/src", "/dst", src, dst, true)().(syncScanMsg)

	if msg.err != nil || len(msg.plan.copies) != 1 {
		t.Fatalf("scan = %+v, %v; want f.txt planned as new", msg.plan.copies, msg.err)
	}
}

// Each prune removal gets its own deadline instead of every one sharing a
// single budget that a long prune outlives.
func TestMirrorPruneGivesEachRemovalItsOwnDeadline(t *testing.T) {
	dst := newDeleteFS().put("/", dirEntry("dst")).put("/dst", fileEntry("a"), fileEntry("b"), fileEntry("c"))
	paths := []prunePath{{path: "/dst/a"}, {path: "/dst/b"}, {path: "/dst/c"}}

	msg := syncPruneCmd(dst, paths)().(syncPruneMsg)

	if msg.removed != 3 {
		t.Fatalf("removed %d, failed %d (%v); want all three", msg.removed, msg.failed, msg.err)
	}
	if len(dst.budgets) != 3 {
		t.Fatalf("budgets = %v, want one deadline per removal", dst.budgets)
	}
	for _, budget := range dst.budgets {
		if budget > deleteStepTimeout {
			t.Fatalf("a removal had %v left, more than the per-step %v: the prune is sharing one long budget", budget, deleteStepTimeout)
		}
	}
}
