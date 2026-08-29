package fakefs

import (
	"context"
	"testing"
	"time"
)

func TestListSortsDirsFirstAndHidesDotfiles(t *testing.T) {
	entries, err := NewRemote().List(context.Background(), "/", false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	seenFile := false
	for _, entry := range entries {
		if entry.Hidden {
			t.Fatalf("hidden entry %q returned with showHidden off", entry.Name)
		}
		if entry.IsDir() && seenFile {
			t.Fatalf("directory %q sorted after a file", entry.Name)
		}
		if !entry.IsDir() {
			seenFile = true
		}
	}
}

func TestListReportsAnUnknownDirectory(t *testing.T) {
	if _, err := NewRemote().List(context.Background(), "/no/such/dir", false); err == nil {
		t.Fatalf("listing an unknown directory returned no error; a real server would fail")
	}
}

func TestListHonoursContextCancellationDuringLatency(t *testing.T) {
	remote := NewRemoteWithLatency(10 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := remote.List(ctx, "/", false); err == nil {
		t.Fatalf("a cancelled listing returned no error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancellation took %v; latency should be interruptible", elapsed)
	}
}

func TestHiddenEntriesAppearWhenAsked(t *testing.T) {
	entries, err := NewRemote().List(context.Background(), "/", true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, entry := range entries {
		if entry.Name == ".server-note" {
			return
		}
	}
	t.Fatalf("showHidden did not include .server-note")
}

func listNames(t *testing.T, r *Remote, dir string) []string {
	t.Helper()
	entries, err := r.List(context.Background(), dir, true)
	if err != nil {
		t.Fatalf("List(%s): %v", dir, err)
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name)
	}
	return out
}

func has(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func TestMkdirAddsAChildDirectory(t *testing.T) {
	r := NewRemote()
	if err := r.Mkdir(context.Background(), "/public_html/drafts"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if !has(listNames(t, r, "/public_html"), "drafts") {
		t.Fatalf("/public_html = %v, want it to include drafts", listNames(t, r, "/public_html"))
	}
	if _, err := r.List(context.Background(), "/public_html/drafts", false); err != nil {
		t.Fatalf("new directory is not listable: %v", err)
	}
	if err := r.Mkdir(context.Background(), "/public_html/drafts"); err == nil {
		t.Fatalf("Mkdir over an existing path returned no error")
	}
}

func TestRenameMovesAFileWithinADirectory(t *testing.T) {
	r := NewRemote()
	if err := r.Rename(context.Background(), "/public_html/robots.txt", "/public_html/humans.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	got := listNames(t, r, "/public_html")
	if has(got, "robots.txt") || !has(got, "humans.txt") {
		t.Fatalf("/public_html = %v, want robots.txt renamed to humans.txt", got)
	}
}

func TestRenameRelocatesADirectorySubtree(t *testing.T) {
	r := NewRemote()
	if err := r.Rename(context.Background(), "/public_html/assets", "/public_html/static"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := r.List(context.Background(), "/public_html/assets", false); err == nil {
		t.Fatalf("old subtree path still listable after rename")
	}
	if !has(listNames(t, r, "/public_html/static"), "bundle.js") {
		t.Fatalf("/public_html/static = %v, want the moved children", listNames(t, r, "/public_html/static"))
	}
}

func TestRenameRefusesAnExistingTarget(t *testing.T) {
	r := NewRemote()
	err := r.Rename(context.Background(), "/public_html/robots.txt", "/public_html/app.css")
	if err == nil {
		t.Fatalf("rename onto an existing name succeeded; want it refused")
	}
	got := listNames(t, r, "/public_html")
	if !has(got, "robots.txt") || !has(got, "app.css") {
		t.Fatalf("/public_html = %v, want both files untouched", got)
	}
}

func TestRemoveRefusesANonEmptyDirectory(t *testing.T) {
	r := NewRemote()
	if err := r.Remove(context.Background(), "/public_html/assets"); err == nil {
		t.Fatalf("removing a non-empty directory succeeded; want it refused")
	}
	if err := r.Remove(context.Background(), "/public_html/robots.txt"); err != nil {
		t.Fatalf("Remove file: %v", err)
	}
	if has(listNames(t, r, "/public_html"), "robots.txt") {
		t.Fatalf("robots.txt still present after Remove")
	}
}
