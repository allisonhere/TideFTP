package localfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"tideftp/internal/domain"
)

func tempTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"beta.txt":   "beta",
		"Alpha.txt":  "alpha",
		".hidden":    "secret",
		"gamma.data": "gamma",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func names(entries []domain.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name)
	}
	return out
}

func TestListSortsDirsFirstAndHidesDotfiles(t *testing.T) {
	root := tempTree(t)

	entries, err := New().List(context.Background(), root, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := names(entries)
	want := []string{"sub", "Alpha.txt", "beta.txt", "gamma.data"}
	if len(got) != len(want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entries = %v, want %v (dirs first, then case-insensitive by name)", got, want)
		}
	}
}

func TestListCanShowHiddenFiles(t *testing.T) {
	root := tempTree(t)

	entries, err := New().List(context.Background(), root, true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found bool
	for _, entry := range entries {
		if entry.Name == ".hidden" {
			found = true
			if !entry.Hidden {
				t.Fatalf(".hidden was not marked hidden")
			}
		}
	}
	if !found {
		t.Fatalf("showHidden did not include .hidden: %v", names(entries))
	}
}

func TestListReportsAMissingDirectory(t *testing.T) {
	if _, err := New().List(context.Background(), filepath.Join(t.TempDir(), "nope"), false); err == nil {
		t.Fatalf("listing a missing directory returned no error")
	}
}

func TestListHonoursContextCancellation(t *testing.T) {
	root := tempTree(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := New().List(ctx, root, false); err == nil {
		t.Fatalf("listing with a cancelled context returned no error")
	}
}

func TestMkdirRenameRemove(t *testing.T) {
	root := tempTree(t)
	fs := New()
	ctx := context.Background()

	if err := fs.Mkdir(ctx, filepath.Join(root, "reports")); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if info, err := os.Stat(filepath.Join(root, "reports")); err != nil || !info.IsDir() {
		t.Fatalf("Stat(reports) = %v, %v; want a directory", info, err)
	}

	if err := fs.Rename(ctx, filepath.Join(root, "beta.txt"), filepath.Join(root, "delta.txt")); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "beta.txt")); !os.IsNotExist(err) {
		t.Fatalf("old name still present after rename: %v", err)
	}

	if err := fs.Remove(ctx, filepath.Join(root, "delta.txt")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "delta.txt")); !os.IsNotExist(err) {
		t.Fatalf("file not removed: %v", err)
	}
}

// TestRenameRefusesToOverwrite pins the cross-backend contract: a rename onto
// an existing name is refused rather than silently replacing it (os.Rename's
// own default on Unix).
func TestRenameRefusesToOverwrite(t *testing.T) {
	root := tempTree(t)

	err := New().Rename(context.Background(), filepath.Join(root, "beta.txt"), filepath.Join(root, "gamma.data"))
	if err == nil {
		t.Fatalf("rename onto an existing file succeeded; want it refused")
	}
	if body, _ := os.ReadFile(filepath.Join(root, "gamma.data")); string(body) != "gamma" {
		t.Fatalf("gamma.data = %q, want it left untouched", body)
	}
}

func TestChildAndParent(t *testing.T) {
	fs := New()
	if got := fs.Child("/home/allie", "Projects"); got != "/home/allie/Projects" {
		t.Fatalf("Child = %q", got)
	}
	if got := fs.Parent("/home/allie/Projects"); got != "/home/allie" {
		t.Fatalf("Parent = %q", got)
	}
	// The root is its own parent, which is how the UI knows to stop.
	if got := fs.Parent("/"); got != "/" {
		t.Fatalf("Parent(\"/\") = %q, want / so the UI stops walking up", got)
	}
}
