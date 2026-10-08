package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tideftp/internal/connect"
	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
	"tideftp/internal/session"
	"tideftp/internal/transfer"
	"tideftp/internal/vfs"
)

// copyEngine is a real, disk-touching transfer.Engine for the CLI tests. The
// shipped fake (internal/faketransfer) moves no bytes — it only emits progress
// — which cannot show that a get wrote the file or a put read it. This one
// copies through the remote vfs.FS and the real local disk, so the assertions
// are about actual bytes rather than which request was constructed.
type copyEngine struct {
	fs     vfs.FS
	events chan transfer.Event
}

func newCopyEngine(fsys vfs.FS) *copyEngine {
	return &copyEngine{fs: fsys, events: make(chan transfer.Event, 16)}
}

func (e *copyEngine) Events() <-chan transfer.Event { return e.events }
func (e *copyEngine) Cancel(int)                    {}
func (e *copyEngine) Close() error                  { return nil }
func (e *copyEngine) Start(req transfer.Request)    { go e.run(req) }

func (e *copyEngine) run(req transfer.Request) {
	sent, err := e.move(req)
	event := transfer.Event{ID: req.ID, BytesDone: sent}
	if err != nil {
		event.Kind = transfer.Failed
		event.Err = err
	} else {
		event.Kind = transfer.Completed
	}
	e.events <- event
}

func (e *copyEngine) move(req transfer.Request) (int64, error) {
	ctx := context.Background()
	if req.Direction == domain.Download {
		src, err := e.fs.Open(ctx, req.Source)
		if err != nil {
			return 0, err
		}
		defer func() { _ = src.Close() }()

		flags := os.O_WRONLY | os.O_CREATE
		if req.Offset == 0 {
			flags |= os.O_TRUNC
		}
		dst, err := os.OpenFile(req.Destination, flags, 0o644)
		if err != nil {
			return 0, err
		}
		defer func() { _ = dst.Close() }()

		if req.Offset > 0 {
			// The fake's remote readers are not Seekers, so skip by reading.
			if _, err := io.CopyN(io.Discard, src, req.Offset); err != nil {
				return 0, err
			}
			if _, err := dst.Seek(req.Offset, io.SeekStart); err != nil {
				return 0, err
			}
		}
		return io.Copy(dst, src)
	}

	data, err := os.ReadFile(req.Source)
	if err != nil {
		return 0, err
	}
	if err := e.fs.WriteFile(ctx, req.Destination, data); err != nil {
		return 0, err
	}
	return int64(len(data)), nil
}

type fakeConn struct {
	fs  vfs.FS
	eng *copyEngine
}

func (c *fakeConn) FS() vfs.FS              { return c.fs }
func (c *fakeConn) Engine() transfer.Engine { return c.eng }
func (c *fakeConn) Done() <-chan error      { return make(chan error, 1) }
func (c *fakeConn) Close() error            { return nil }

type fakeDialer struct{ conn session.Conn }

func (d *fakeDialer) Dial(context.Context, session.Target, session.Credentials) (session.Conn, error) {
	return d.conn, nil
}

// testApp wires an App to a fakefs remote and the copy engine, with no
// keyring and a config path that does not exist.
func testApp(t *testing.T, remote *fakefs.Remote) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errOut bytes.Buffer
	conn := &fakeConn{fs: remote, eng: newCopyEngine(remote)}
	app := &App{
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errOut,
		Dial: func(connect.Options) (session.Dialer, error) {
			return &fakeDialer{conn: conn}, nil
		},
		ConfigPath: filepath.Join(t.TempDir(), "no-config.toml"),
	}
	return app, &out, &errOut
}

func run(t *testing.T, remote *fakefs.Remote, args ...string) (int, string, string) {
	t.Helper()
	app, out, errOut := testApp(t, remote)
	full := append([]string{args[0], "--host", "test"}, args[1:]...)
	code := app.Run(full)
	return code, out.String(), errOut.String()
}

// withArgs lets a test omit the default --host, e.g. to check that a missing
// one is a usage error.
func runRaw(t *testing.T, remote *fakefs.Remote, args ...string) (int, string, string) {
	t.Helper()
	app, out, errOut := testApp(t, remote)
	code := app.Run(args)
	return code, out.String(), errOut.String()
}

func TestLsListsDirectory(t *testing.T) {
	code, out, errOut := run(t, fakefs.NewRemote(), "ls", "/")
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	for _, want := range []string{"welcome.txt", "incoming", "public_html"} {
		if !strings.Contains(out, want) {
			t.Errorf("ls output missing %q:\n%s", want, out)
		}
	}
}

func TestGetDownloadsAFile(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "welcome.txt")

	code, _, errOut := run(t, fakefs.NewRemote(), "get", "/welcome.txt", dest)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "Welcome to the fake server.\n" {
		t.Fatalf("downloaded %q, want the remote body", got)
	}
}

func TestGetRefusesToOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "welcome.txt")
	if err := os.WriteFile(dest, []byte("do not clobber"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, errOut := run(t, fakefs.NewRemote(), "get", "/welcome.txt", dest)
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stderr=%s", code, errOut)
	}
	if got, _ := os.ReadFile(dest); string(got) != "do not clobber" {
		t.Fatalf("destination was overwritten despite the refusal: %q", got)
	}

	code, _, errOut = run(t, fakefs.NewRemote(), "get", "--force", "/welcome.txt", dest)
	if code != 0 {
		t.Fatalf("--force exit = %d, stderr=%s", code, errOut)
	}
	if got, _ := os.ReadFile(dest); string(got) != "Welcome to the fake server.\n" {
		t.Fatalf("--force left %q, want the remote body", got)
	}
}

func TestGetResumeContinuesAPartialFile(t *testing.T) {
	remote := fakefs.NewRemote()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "src.txt")
	if err := os.WriteFile(src, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := run(t, remote, "put", src, "/src.txt"); code != 0 {
		t.Fatalf("seed put exit = %d, stderr=%s", code, errOut)
	}

	dest := filepath.Join(t.TempDir(), "out.txt")
	if err := os.WriteFile(dest, []byte("01234"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, errOut := run(t, remote, "get", "--resume", "/src.txt", dest)
	if code != 0 {
		t.Fatalf("resume exit = %d, stderr=%s", code, errOut)
	}
	if got, _ := os.ReadFile(dest); string(got) != "0123456789" {
		t.Fatalf("resumed file = %q, want the full body", got)
	}
}

func TestGetDirectoryNeedsRecursive(t *testing.T) {
	code, _, errOut := run(t, fakefs.NewRemote(), "get", "/releases/2026-08-ship", t.TempDir())
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage); stderr=%s", code, errOut)
	}
	if !strings.Contains(errOut, "directory") {
		t.Fatalf("stderr = %q, want a hint that -r is needed", errOut)
	}
}

func TestGetRecursiveDownloadsTree(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "ship")
	code, _, errOut := run(t, fakefs.NewRemote(), "get", "-r", "/releases/2026-08-ship", dest)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	for _, name := range []string{"tideftp-linux-amd64.tar.gz", "checksums.txt"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Errorf("%s not downloaded: %v", name, err)
		}
	}
}

func TestPutUploadsAFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(src, []byte("hello remote"), 0o644); err != nil {
		t.Fatal(err)
	}

	remote := fakefs.NewRemote()
	code, _, errOut := run(t, remote, "put", src, "/incoming/hello.txt")
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	got, err := remote.ReadFile(context.Background(), "/incoming/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello remote" {
		t.Fatalf("remote body = %q, want the uploaded content", got)
	}
}

func TestPutRefusesExistingRemoteWithoutForce(t *testing.T) {
	src := filepath.Join(t.TempDir(), "robots.txt")
	if err := os.WriteFile(src, []byte("replace?"), 0o644); err != nil {
		t.Fatal(err)
	}

	remote := fakefs.NewRemote()
	code, _, errOut := run(t, remote, "put", src, "/public_html/robots.txt")
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stderr=%s", code, errOut)
	}
	got, _ := remote.ReadFile(context.Background(), "/public_html/robots.txt")
	if string(got) == "replace?" {
		t.Fatal("an existing remote file was overwritten despite the refusal")
	}

	if code, _, errOut := run(t, remote, "put", "--force", src, "/public_html/robots.txt"); code != 0 {
		t.Fatalf("--force exit = %d, stderr=%s", code, errOut)
	}
}

func TestPutRecursiveUploadsTree(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "top.txt"), []byte("top"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "deep.txt"), []byte("deep"), 0o644); err != nil {
		t.Fatal(err)
	}

	remote := fakefs.NewRemote()
	code, _, errOut := run(t, remote, "put", "-r", root, "/up")
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	for path, want := range map[string]string{
		"/up/top.txt":      "top",
		"/up/sub/deep.txt": "deep",
	} {
		got, err := remote.ReadFile(context.Background(), path)
		if err != nil {
			t.Errorf("%s not uploaded: %v", path, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
}

func TestMkdirCreatesDirectory(t *testing.T) {
	remote := fakefs.NewRemote()
	code, _, errOut := run(t, remote, "mkdir", "/newdir")
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	if _, err := remote.Stat(context.Background(), "/newdir"); err != nil {
		t.Fatalf("directory not created: %v", err)
	}
}

func TestMvRenamesAPath(t *testing.T) {
	remote := fakefs.NewRemote()
	code, _, errOut := run(t, remote, "mv", "/incoming/client-drop.zip", "/incoming/moved.zip")
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	if _, err := remote.Stat(context.Background(), "/incoming/moved.zip"); err != nil {
		t.Fatalf("renamed path missing: %v", err)
	}
	if _, err := remote.Stat(context.Background(), "/incoming/client-drop.zip"); err == nil {
		t.Fatal("the old path still exists after mv")
	}
}

func TestRmDeletesAFile(t *testing.T) {
	remote := fakefs.NewRemote()
	code, _, errOut := run(t, remote, "rm", "/incoming/.partial-upload")
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	if _, err := remote.Stat(context.Background(), "/incoming/.partial-upload"); err == nil {
		t.Fatal("the file still exists after rm")
	}
}

func TestRmDirectoryNeedsRecursive(t *testing.T) {
	remote := fakefs.NewRemote()
	code, _, errOut := run(t, remote, "rm", "/releases")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage); stderr=%s", code, errOut)
	}
	if !strings.Contains(errOut, "directory") {
		t.Fatalf("stderr = %q, want a hint that -r is needed", errOut)
	}
}

func TestRmRecursiveDeletesATree(t *testing.T) {
	remote := fakefs.NewRemote()
	code, _, errOut := run(t, remote, "rm", "-r", "/releases/2026-08-ship")
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	if _, err := remote.Stat(context.Background(), "/releases/2026-08-ship"); err == nil {
		t.Fatal("the directory still exists after rm -r")
	}
}

func TestUnknownCommandExitsTwo(t *testing.T) {
	code, _, _ := runRaw(t, fakefs.NewRemote(), "frobnicate")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestMissingHostIsAUsageError(t *testing.T) {
	code, _, errOut := runRaw(t, fakefs.NewRemote(), "ls", "/")
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr=%s", code, errOut)
	}
	if !strings.Contains(errOut, "host") {
		t.Fatalf("stderr = %q, want a hint that a host is required", errOut)
	}
}

func TestQuietSuppressesResultLines(t *testing.T) {
	code, out, errOut := run(t, fakefs.NewRemote(), "ls", "-q", "/")
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want nothing under -q", out)
	}
}

func TestIsCommand(t *testing.T) {
	if !IsCommand([]string{"tideftp", "get", "/a"}) {
		t.Error("get should be recognised as a subcommand")
	}
	if !IsCommand([]string{"tideftp", "frobnicate"}) {
		t.Error("an unknown word is still command-shaped and must reach the CLI's error")
	}
	if IsCommand([]string{"tideftp", "--host", "h"}) {
		t.Error("a flag is not a subcommand; the interactive app must still start")
	}
	if IsCommand([]string{"tideftp"}) {
		t.Error("no argument is not a subcommand")
	}
}
