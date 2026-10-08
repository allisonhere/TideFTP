package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tideftp/internal/connect"
	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
	"tideftp/internal/session"
	"tideftp/internal/transfer"
	"tideftp/internal/vfs"
)

// hostDialer hands out a fresh connection (with its own engine) per Dial,
// routed by target host, which is what a real dialer does and what the
// parallel workers and remote-to-remote sync depend on.
type hostDialer struct {
	mu      sync.Mutex
	remotes map[string]*fakefs.Remote
	locks   map[string]*sync.Mutex
}

func (d *hostDialer) Dial(_ context.Context, t session.Target, _ session.Credentials) (session.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := &lockedFS{FS: d.remotes[t.Host], mu: d.lockFor(t.Host)}
	return &fakeConn{fs: r, eng: newCopyEngine(r)}, nil
}

func (d *hostDialer) lockFor(host string) *sync.Mutex {
	if d.locks == nil {
		d.locks = map[string]*sync.Mutex{}
	}
	if d.locks[host] == nil {
		d.locks[host] = &sync.Mutex{}
	}
	return d.locks[host]
}

// lockedFS serialises access to a fakefs.Remote, which is not safe for the
// concurrent workers a real server would serve.
type lockedFS struct {
	vfs.FS
	mu *sync.Mutex
}

func (l *lockedFS) List(ctx context.Context, p string, h bool) ([]domain.Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.FS.List(ctx, p, h)
}
func (l *lockedFS) Stat(ctx context.Context, p string) (domain.Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.FS.Stat(ctx, p)
}
func (l *lockedFS) Mkdir(ctx context.Context, p string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.FS.Mkdir(ctx, p)
}
func (l *lockedFS) Rename(ctx context.Context, a, b string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.FS.Rename(ctx, a, b)
}
func (l *lockedFS) Remove(ctx context.Context, p string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.FS.Remove(ctx, p)
}
func (l *lockedFS) ReadFile(ctx context.Context, p string) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.FS.ReadFile(ctx, p)
}
func (l *lockedFS) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.FS.Open(ctx, p)
}
func (l *lockedFS) WriteFile(ctx context.Context, p string, b []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.FS.WriteFile(ctx, p, b)
}

func syncApp(t *testing.T, remotes map[string]*fakefs.Remote) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errOut bytes.Buffer
	cfg := filepath.Join(t.TempDir(), "config.toml")
	var b strings.Builder
	for name := range remotes {
		b.WriteString("[[profiles]]\nname = \"" + name + "\"\nprotocol = \"sftp\"\nhost = \"" + name + "\"\nuser = \"u\"\nstart_path = \"/\"\n\n")
	}
	if err := os.WriteFile(cfg, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &hostDialer{remotes: remotes}
	return &App{
		Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut,
		Dial:       func(connect.Options) (session.Dialer, error) { return d, nil },
		ConfigPath: cfg,
	}, &out, &errOut
}

func syncRunCLI(t *testing.T, remotes map[string]*fakefs.Remote, args ...string) (int, string, string) {
	t.Helper()
	app, out, errOut := syncApp(t, remotes)
	code := app.Run(append([]string{"sync"}, args...))
	return code, out.String(), errOut.String()
}

func writeLocal(t *testing.T, root, rel, body string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readRemote(t *testing.T, r *fakefs.Remote, p string) string {
	t.Helper()
	b, err := r.ReadFile(context.Background(), p)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func hosts(r *fakefs.Remote) map[string]*fakefs.Remote { return map[string]*fakefs.Remote{"alpha": r} }

func TestParseLocation(t *testing.T) {
	cases := []struct {
		in   string
		want location
	}{
		{"./www", location{path: "./www"}},
		{"/abs/path", location{path: "/abs/path"}},
		{"prod:/var/www", location{remote: true, profile: "prod", path: "/var/www"}},
		{":/var/www", location{remote: true, path: "/var/www"}},
		{"prod:", location{remote: true, profile: "prod"}},
		{`C:\data`, location{path: `C:\data`}},
		{"./a:b", location{path: "./a:b"}},
		{"/srv/a:b", location{path: "/srv/a:b"}},
	}
	for _, tc := range cases {
		if got := parseLocation(tc.in); got != tc.want {
			t.Errorf("parseLocation(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestParseSizeAndAge(t *testing.T) {
	if n, err := parseSize("1.5k"); err != nil || n != 1536 {
		t.Errorf("parseSize(1.5k) = %d, %v", n, err)
	}
	if n, err := parseSize("2M"); err != nil || n != 2<<20 {
		t.Errorf("parseSize(2M) = %d, %v", n, err)
	}
	if _, err := parseSize("abc"); err == nil {
		t.Error("parseSize(abc) should fail")
	}
	if d, err := parseAge("2d"); err != nil || d != 48*time.Hour {
		t.Errorf("parseAge(2d) = %v, %v", d, err)
	}
	if d, err := parseAge("90m"); err != nil || d != 90*time.Minute {
		t.Errorf("parseAge(90m) = %v, %v", d, err)
	}
}

func TestSyncUploadsThenIsIdempotent(t *testing.T) {
	remote := fakefs.NewRemote()
	src := t.TempDir()
	writeLocal(t, src, "a.txt", "A")
	writeLocal(t, src, "sub/deep/b.txt", "B")

	code, out, errOut := syncRunCLI(t, hosts(remote), src, "alpha:/incoming/site")
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	if readRemote(t, remote, "/incoming/site/a.txt") != "A" || readRemote(t, remote, "/incoming/site/sub/deep/b.txt") != "B" {
		t.Fatalf("files not uploaded; out=%s", out)
	}
	if readRemote(t, remote, "/incoming/site/a.txt.part") != "<missing>" {
		t.Fatalf(".part left behind")
	}

	code, out, errOut = syncRunCLI(t, hosts(remote), src, "alpha:/incoming/site")
	if code != 0 || strings.Contains(out, "copy") || strings.Contains(out, "update") {
		t.Fatalf("second run should do nothing: exit=%d out=%s", code, out)
	}
	if !strings.Contains(errOut, "2 unchanged") {
		t.Fatalf("summary = %q, want 2 unchanged", errOut)
	}
}

func TestSyncDryRunChangesNothing(t *testing.T) {
	remote := fakefs.NewRemote()
	src := t.TempDir()
	writeLocal(t, src, "a.txt", "A")
	code, out, errOut := syncRunCLI(t, hosts(remote), "--dry-run", src, "alpha:/incoming/dry")
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "copy   a.txt") {
		t.Fatalf("dry run should list the copy: %q", out)
	}
	if readRemote(t, remote, "/incoming/dry/a.txt") != "<missing>" {
		t.Fatalf("dry run uploaded a file")
	}
	if code, _, _ := run(t, remote, "exists", "/incoming/dry"); code != 5 {
		t.Fatalf("dry run created the destination directory")
	}
}

func TestSyncUpdatesChangedSize(t *testing.T) {
	remote := fakefs.NewRemote()
	src := t.TempDir()
	p := writeLocal(t, src, "a.txt", "one")
	if code, _, e := syncRunCLI(t, hosts(remote), src, "alpha:/incoming/u"); code != 0 {
		t.Fatalf("seed: %s", e)
	}
	_ = os.WriteFile(p, []byte("longer body"), 0o644)
	code, out, errOut := syncRunCLI(t, hosts(remote), src, "alpha:/incoming/u")
	if code != 0 || !strings.Contains(out, "update") {
		t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
	}
	if readRemote(t, remote, "/incoming/u/a.txt") != "longer body" {
		t.Fatalf("update not applied")
	}
}

func TestSyncChecksumCatchesSameSizeChange(t *testing.T) {
	remote := fakefs.NewRemote()
	src := t.TempDir()
	p := writeLocal(t, src, "a.txt", "aaaa")
	if code, _, e := syncRunCLI(t, hosts(remote), src, "alpha:/incoming/c"); code != 0 {
		t.Fatalf("seed: %s", e)
	}
	_ = os.WriteFile(p, []byte("bbbb"), 0o644)
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(p, old, old)

	if code, out, _ := syncRunCLI(t, hosts(remote), src, "alpha:/incoming/c"); code != 0 || strings.Contains(out, "update") {
		t.Fatalf("default mode should miss a same-size, not-newer change; out=%s", out)
	}
	if code, out, _ := syncRunCLI(t, hosts(remote), "--checksum", src, "alpha:/incoming/c"); code != 0 || !strings.Contains(out, "update") {
		t.Fatalf("--checksum should catch it; out=%s", out)
	}
	if readRemote(t, remote, "/incoming/c/a.txt") != "bbbb" {
		t.Fatalf("checksum update not applied")
	}
}

func TestSyncDeleteIsOptIn(t *testing.T) {
	remote := fakefs.NewRemote()
	src := t.TempDir()
	writeLocal(t, src, "keep.txt", "k")
	_ = remote.Mkdir(context.Background(), "/incoming/d")
	_ = remote.Mkdir(context.Background(), "/incoming/d/olddir")
	_ = remote.WriteFile(context.Background(), "/incoming/d/stale.txt", []byte("s"))
	_ = remote.WriteFile(context.Background(), "/incoming/d/olddir/x.txt", []byte("x"))

	if code, _, e := syncRunCLI(t, hosts(remote), src, "alpha:/incoming/d"); code != 0 {
		t.Fatalf("exit: %s", e)
	}
	if readRemote(t, remote, "/incoming/d/stale.txt") != "s" {
		t.Fatalf("a sync without --delete removed a file")
	}
	code, out, e := syncRunCLI(t, hosts(remote), "--delete", src, "alpha:/incoming/d")
	if code != 0 {
		t.Fatalf("exit=%d %s", code, e)
	}
	if !strings.Contains(out, "stale.txt") {
		t.Fatalf("out = %s", out)
	}
	for _, p := range []string{"/incoming/d/stale.txt", "/incoming/d/olddir/x.txt"} {
		if readRemote(t, remote, p) != "<missing>" {
			t.Errorf("%s survived --delete", p)
		}
	}
	if code, _, _ := run(t, remote, "exists", "/incoming/d/olddir"); code != 5 {
		t.Errorf("empty olddir survived --delete")
	}
	if readRemote(t, remote, "/incoming/d/keep.txt") != "k" {
		t.Errorf("keep.txt lost")
	}
}

func TestSyncDeleteRefusesEmptySource(t *testing.T) {
	remote := fakefs.NewRemote()
	_ = remote.Mkdir(context.Background(), "/incoming/e")
	_ = remote.WriteFile(context.Background(), "/incoming/e/f.txt", []byte("f"))
	empty := t.TempDir()
	if code, _, _ := syncRunCLI(t, hosts(remote), "--delete", empty, "alpha:/incoming/e"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if readRemote(t, remote, "/incoming/e/f.txt") != "f" {
		t.Fatalf("file deleted despite the guard")
	}
	if code, _, _ := syncRunCLI(t, hosts(remote), "--delete", "--allow-empty-source", empty, "alpha:/incoming/e"); code != 0 {
		t.Fatalf("--allow-empty-source should permit it")
	}
}

func TestSyncDownloadKeepsMtime(t *testing.T) {
	remote := fakefs.NewRemote()
	dest := t.TempDir()
	// The fake's canned files report sizes unrelated to their bodies, which
	// would look like a change on every run; files written here are honest.
	_ = remote.Mkdir(context.Background(), "/incoming/dl")
	_ = remote.WriteFile(context.Background(), "/incoming/dl/robots.txt", []byte("User-agent: *"))

	code, _, errOut := syncRunCLI(t, hosts(remote), "alpha:/incoming/dl", dest)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "robots.txt")); err != nil || string(got) != "User-agent: *" {
		t.Fatalf("robots.txt = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "robots.txt.part")); err == nil {
		t.Fatalf(".part left behind")
	}
	code, out, e := syncRunCLI(t, hosts(remote), "alpha:/incoming/dl", dest)
	if code != 0 || strings.Contains(out, "copy") || strings.Contains(out, "update") {
		t.Fatalf("second download should be a no-op: exit=%d out=%s err=%s", code, out, e)
	}
}

func TestSyncRemoteToRemote(t *testing.T) {
	a, b := fakefs.NewRemote(), fakefs.NewRemote()
	_ = a.Mkdir(context.Background(), "/incoming/src")
	_ = a.WriteFile(context.Background(), "/incoming/src/f.txt", []byte("relay me"))
	code, _, errOut := syncRunCLI(t, map[string]*fakefs.Remote{"alpha": a, "beta": b}, "alpha:/incoming/src", "beta:/incoming/dst")
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	if readRemote(t, b, "/incoming/dst/f.txt") != "relay me" {
		t.Fatalf("not relayed")
	}
}

func TestSyncFilters(t *testing.T) {
	remote := fakefs.NewRemote()
	src := t.TempDir()
	writeLocal(t, src, "keep.go", "go")
	writeLocal(t, src, "skip.log", "log")
	writeLocal(t, src, "node_modules/x.go", "dep")
	writeLocal(t, src, "big.go", strings.Repeat("x", 100))

	code, _, errOut := syncRunCLI(t, hosts(remote), "--include", "*.go", "--exclude", "node_modules", "--max-size", "50", src, "alpha:/incoming/f")
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	if readRemote(t, remote, "/incoming/f/keep.go") != "go" {
		t.Errorf("keep.go missing")
	}
	for _, p := range []string{"skip.log", "node_modules/x.go", "big.go"} {
		if readRemote(t, remote, "/incoming/f/"+p) != "<missing>" {
			t.Errorf("%s should have been filtered", p)
		}
	}
}

func TestSyncMaxAgeDoesNotDeleteFilteredOutFiles(t *testing.T) {
	remote := fakefs.NewRemote()
	src := t.TempDir()
	p := writeLocal(t, src, "old.txt", "old")
	old := time.Now().Add(-30 * 24 * time.Hour)
	_ = os.Chtimes(p, old, old)
	writeLocal(t, src, "new.txt", "new")
	_ = remote.Mkdir(context.Background(), "/incoming/g")
	_ = remote.WriteFile(context.Background(), "/incoming/g/old.txt", []byte("old"))

	code, _, errOut := syncRunCLI(t, hosts(remote), "--delete", "--max-age", "7d", src, "alpha:/incoming/g")
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	if readRemote(t, remote, "/incoming/g/old.txt") != "old" {
		t.Fatalf("--max-age made --delete remove a file that still exists in the source")
	}
	if readRemote(t, remote, "/incoming/g/new.txt") != "new" {
		t.Fatalf("new.txt not copied")
	}
}

func TestSyncParallelTransfers(t *testing.T) {
	remote := fakefs.NewRemote()
	src := t.TempDir()
	for i := 0; i < 12; i++ {
		writeLocal(t, src, filepath.Join("d", string(rune('a'+i))+".txt"), string(rune('A'+i)))
	}
	code, _, errOut := syncRunCLI(t, hosts(remote), "--transfers", "4", src, "alpha:/incoming/p")
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	for i := 0; i < 12; i++ {
		if got := readRemote(t, remote, "/incoming/p/d/"+string(rune('a'+i))+".txt"); got != string(rune('A'+i)) {
			t.Errorf("file %d = %q", i, got)
		}
	}
}

func TestSyncUsageErrors(t *testing.T) {
	remote := fakefs.NewRemote()
	if code, _, _ := syncRunCLI(t, hosts(remote), t.TempDir(), t.TempDir()); code != 2 {
		t.Errorf("local→local = %d, want 2", code)
	}
	if code, _, _ := syncRunCLI(t, hosts(remote), "only-one"); code != 2 {
		t.Errorf("one operand = %d, want 2", code)
	}
	if code, _, _ := syncRunCLI(t, hosts(remote), "--checksum", "--size-only", t.TempDir(), "alpha:/x"); code != 2 {
		t.Errorf("checksum+size-only = %d, want 2", code)
	}
	if code, _, _ := syncRunCLI(t, hosts(remote), t.TempDir(), "nosuch:/x"); code != 1 {
		t.Errorf("unknown profile = %d, want 1", code)
	}
}

var _ transfer.Engine = (*copyEngine)(nil)
