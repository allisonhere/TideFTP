package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tideftp/internal/fakefs"
)

func mirrorCLI(t *testing.T, remote *fakefs.Remote, args ...string) (int, string, string) {
	t.Helper()
	app, out, errOut := syncApp(t, hosts(remote))
	code := app.Run(append([]string{"mirror"}, args...))
	return code, out.String(), errOut.String()
}

func seedRemoteTree(t *testing.T, r *fakefs.Remote) {
	t.Helper()
	ctx := context.Background()
	_ = r.Mkdir(ctx, "/incoming/data")
	_ = r.Mkdir(ctx, "/incoming/data/logs")
	_ = r.Mkdir(ctx, "/incoming/data/empty")
	_ = r.WriteFile(ctx, "/incoming/data/a.txt", []byte("aaa"))
	_ = r.WriteFile(ctx, "/incoming/data/b.log", []byte("bbbb"))
	_ = r.WriteFile(ctx, "/incoming/data/logs/c.log", []byte("cc"))
}

func TestMirrorDownloadsAndDefaultsTheLocalName(t *testing.T) {
	remote := fakefs.NewRemote()
	seedRemoteTree(t, remote)
	t.Chdir(t.TempDir())

	if code, _, e := mirrorCLI(t, remote, "alpha:/incoming/data"); code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	// With no LOCAL, the local directory is named after the remote one.
	if b, _ := os.ReadFile("data/logs/c.log"); string(b) != "cc" {
		t.Fatalf("mirror did not download into ./data")
	}
	if _, err := os.Stat("data/empty"); err != nil {
		t.Fatalf("empty directories are mirrored by default: %v", err)
	}
	dest := t.TempDir()
	if code, _, e := mirrorCLI(t, remote, "alpha:/incoming/data", dest); code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "a.txt")); string(b) != "aaa" {
		t.Fatalf("explicit LOCAL ignored")
	}
}

func TestMirrorReverseUploads(t *testing.T) {
	remote := fakefs.NewRemote()
	src := t.TempDir()
	writeLocal(t, src, "up.txt", "UP")
	writeLocal(t, src, "deep/x.txt", "X")
	if code, _, e := mirrorCLI(t, remote, "-R", src, "alpha:/incoming/uploaded"); code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if readRemote(t, remote, "/incoming/uploaded/deep/x.txt") != "X" {
		t.Fatalf("mirror -R did not upload")
	}
}

func TestMirrorLftpFilterNames(t *testing.T) {
	remote := fakefs.NewRemote()
	seedRemoteTree(t, remote)

	// -x is a REGEX in mirror (lftp), -X a glob.
	dest := t.TempDir()
	if code, _, e := mirrorCLI(t, remote, "-x", `\.log$`, "alpha:/incoming/data", dest); code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if _, err := os.Stat(filepath.Join(dest, "b.log")); err == nil {
		t.Fatalf("-x '\\.log$' should exclude b.log")
	}
	if _, err := os.Stat(filepath.Join(dest, "a.txt")); err != nil {
		t.Fatalf("a.txt wrongly excluded")
	}
	dest = t.TempDir()
	if code, _, e := mirrorCLI(t, remote, "-X", "*.txt", "alpha:/incoming/data", dest); code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if _, err := os.Stat(filepath.Join(dest, "a.txt")); err == nil {
		t.Fatalf("-X '*.txt' should exclude a.txt")
	}
	dest = t.TempDir()
	if code, _, _ := mirrorCLI(t, remote, "--include", `logs/`, "alpha:/incoming/data", dest); code != 0 {
		t.Fatalf("include regex failed")
	}
	if _, err := os.Stat(filepath.Join(dest, "logs", "c.log")); err != nil {
		t.Fatalf("--include 'logs/' should keep logs/c.log")
	}
	if _, err := os.Stat(filepath.Join(dest, "a.txt")); err == nil {
		t.Fatalf("--include 'logs/' should drop a.txt")
	}
	if code, _, _ := mirrorCLI(t, remote, "-x", `([`, "alpha:/incoming/data", t.TempDir()); code != 2 {
		t.Fatalf("a bad regex should be a usage error")
	}
}

func TestMirrorNoEmptyDirsAndNoRecursion(t *testing.T) {
	remote := fakefs.NewRemote()
	seedRemoteTree(t, remote)
	dest := t.TempDir()
	if code, _, e := mirrorCLI(t, remote, "--no-empty-dirs", "alpha:/incoming/data", dest); code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if _, err := os.Stat(filepath.Join(dest, "empty")); err == nil {
		t.Fatalf("--no-empty-dirs still created the empty directory")
	}
	dest = t.TempDir()
	if code, _, e := mirrorCLI(t, remote, "-r", "alpha:/incoming/data", dest); code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if _, err := os.Stat(filepath.Join(dest, "logs")); err == nil {
		t.Fatalf("-r (no recursion) entered a subdirectory")
	}
	if _, err := os.Stat(filepath.Join(dest, "a.txt")); err != nil {
		t.Fatalf("-r should still mirror the top level")
	}
}

func TestMirrorOnlyMissingAndOnlyNewer(t *testing.T) {
	remote := fakefs.NewRemote()
	src := t.TempDir()
	p := writeLocal(t, src, "f.txt", "version-1")
	if code, _, e := mirrorCLI(t, remote, "-R", src, "alpha:/incoming/o"); code != 0 {
		t.Fatalf("seed: %s", e)
	}
	// Changed locally (different size) and given a newer mtime.
	_ = os.WriteFile(p, []byte("version-two!"), 0o644)
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(p, future, future)
	writeLocal(t, src, "new.txt", "N")

	code, out, _ := mirrorCLI(t, remote, "-R", "--only-missing", src, "alpha:/incoming/o")
	if code != 0 || strings.Contains(out, "update") || !strings.Contains(out, "new.txt") {
		t.Fatalf("--only-missing: out=%q", out)
	}
	if readRemote(t, remote, "/incoming/o/f.txt") != "version-1" {
		t.Fatalf("--only-missing replaced an existing file")
	}
	// Same size, source older: -n must not touch it. Newer: it updates.
	code, out, _ = mirrorCLI(t, remote, "-R", "-n", src, "alpha:/incoming/o")
	if code != 0 || !strings.Contains(out, "update f.txt") {
		t.Fatalf("-n should update a newer source: %q", out)
	}
	if readRemote(t, remote, "/incoming/o/f.txt") != "version-two!" {
		t.Fatalf("-n did not update")
	}
}

func TestMirrorMaxErrorsAndDeleteFirstAndOnChange(t *testing.T) {
	remote := fakefs.NewRemote()
	src := t.TempDir()
	writeLocal(t, src, "keep.txt", "k")
	_ = remote.Mkdir(context.Background(), "/incoming/z")
	_ = remote.WriteFile(context.Background(), "/incoming/z/stale.txt", []byte("s"))

	marker := filepath.Join(t.TempDir(), "changed")
	logf := filepath.Join(t.TempDir(), "mirror.log")
	code, out, e := mirrorCLI(t, remote, "-R", "-e", "--delete-first", "--on-change", "echo hi > "+marker, "--log", logf, src, "alpha:/incoming/z")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if !strings.Contains(out, "stale.txt") || readRemote(t, remote, "/incoming/z/stale.txt") != "<missing>" {
		t.Fatalf("-e with --delete-first did not delete: %q", out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("--on-change did not run after changes: %v", err)
	}
	if b, _ := os.ReadFile(logf); !strings.Contains(string(b), "keep.txt") {
		t.Fatalf("--log missing the copy: %q", b)
	}
	// Nothing changed: --on-change must not fire again.
	_ = os.Remove(marker)
	if code, _, _ := mirrorCLI(t, remote, "-R", "-e", "--on-change", "echo hi > "+marker, src, "alpha:/incoming/z"); code != 0 {
		t.Fatalf("second run failed")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("--on-change fired although nothing changed")
	}
}

func TestMirrorNewerThanAndVerifyAndDereference(t *testing.T) {
	remote := fakefs.NewRemote()
	src := t.TempDir()
	old := writeLocal(t, src, "old.txt", "o")
	past := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(old, past, past)
	writeLocal(t, src, "fresh.txt", "f")

	cutoff := time.Now().Add(-24 * time.Hour).Format("2006-01-02T15:04:05")
	if code, _, e := mirrorCLI(t, remote, "-R", "--newer-than", cutoff, "--verify", src, "alpha:/incoming/n"); code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if readRemote(t, remote, "/incoming/n/fresh.txt") != "f" || readRemote(t, remote, "/incoming/n/old.txt") != "<missing>" {
		t.Fatalf("--newer-than filtered wrongly")
	}
	if code, _, _ := mirrorCLI(t, remote, "-R", "--newer-than", "not-a-date", src, "alpha:/incoming/n"); code != 2 {
		t.Fatalf("a bad --newer-than should be a usage error")
	}

	// A symlinked directory is skipped unless -L is given.
	real := t.TempDir()
	writeLocal(t, real, "inner.txt", "I")
	if err := os.Symlink(real, filepath.Join(src, "link")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if code, _, e := mirrorCLI(t, remote, "-R", src, "alpha:/incoming/nolink"); code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if readRemote(t, remote, "/incoming/nolink/link/inner.txt") != "<missing>" {
		t.Fatalf("a symlinked directory was followed without -L")
	}
	if code, _, e := mirrorCLI(t, remote, "-R", "-L", src, "alpha:/incoming/link"); code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if readRemote(t, remote, "/incoming/link/link/inner.txt") != "I" {
		t.Fatalf("-L did not follow the symlinked directory")
	}
}

func TestMirrorUploadPreservesMtimeAndMode(t *testing.T) {
	remote := fakefs.NewRemote()
	src := t.TempDir()
	p := writeLocal(t, src, "m.txt", "m")
	stamp := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	_ = os.Chtimes(p, stamp, stamp)
	_ = os.Chmod(p, 0o640)
	if code, _, e := mirrorCLI(t, remote, "-R", src, "alpha:/incoming/meta"); code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	e, err := remote.Stat(context.Background(), "/incoming/meta/m.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !e.Modified.Equal(stamp) {
		t.Fatalf("remote mtime = %v, want the source's %v", e.Modified, stamp)
	}
	if !strings.HasSuffix(e.Mode, "rw-r-----") {
		t.Fatalf("remote mode = %q, want rw-r-----", e.Mode)
	}
	// --no-perms leaves the mode at whatever the server gave the new file.
	if code, _, _ := mirrorCLI(t, remote, "-R", "-p", src, "alpha:/incoming/meta2"); code != 0 {
		t.Fatalf("-p failed")
	}
	e2, _ := remote.Stat(context.Background(), "/incoming/meta2/m.txt")
	if strings.HasSuffix(e2.Mode, "rw-r-----") {
		t.Fatalf("--no-perms still copied the mode (%q)", e2.Mode)
	}
}

func TestMirrorInsideAScriptUsesTheSharedConnection(t *testing.T) {
	remote := fakefs.NewRemote()
	seedRemoteTree(t, remote)
	local := t.TempDir()
	app, dials, _, errOut := countingApp(t, remote)
	script := "lcd " + local + "\ncd /incoming\nmirror --parallel 1 data got\n"
	// countingApp serves host "x" by route-less fake; mirror takes a plain path.
	if code := app.Run([]string{"script", "--host", "x", "-c", script}); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if b, _ := os.ReadFile(filepath.Join(local, "got", "logs", "c.log")); string(b) != "cc" {
		t.Fatalf("mirror inside a script did not download relative to the cwd")
	}
	if *dials != 1 {
		t.Fatalf("dialled %d times, want the script's single connection", *dials)
	}
}
