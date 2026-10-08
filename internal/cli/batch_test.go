package cli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"tideftp/internal/connect"
	"tideftp/internal/fakefs"
	"tideftp/internal/session"
)

func TestSplitCommands(t *testing.T) {
	cases := []struct {
		in   string
		want [][]string
		bad  bool
	}{
		{`get a.txt`, [][]string{{"get", "a.txt"}}, false},
		{`put 'my file.txt' "/dir/with space/"`, [][]string{{"put", "my file.txt", "/dir/with space/"}}, false},
		{`cd /x; ls -l ; pwd`, [][]string{{"cd", "/x"}, {"ls", "-l"}, {"pwd"}}, false},
		{`echo "a;b" 'c;d' e\;f`, [][]string{{"echo", "a;b", "c;d", "e;f"}}, false},
		{`ls # a comment`, [][]string{{"ls"}}, false},
		{`ls a#b`, [][]string{{"ls", "a#b"}}, false},
		{`# only a comment`, nil, false},
		{`mv "a \"q\" b" c`, [][]string{{"mv", `a "q" b`, "c"}}, false},
		{`put ''`, [][]string{{"put", ""}}, false},
		{`get 'oops`, nil, true},
		{`get oops\`, nil, true},
	}
	for _, tc := range cases {
		got, err := splitCommands(tc.in)
		if (err != nil) != tc.bad {
			t.Errorf("%q: err = %v, want error=%v", tc.in, err, tc.bad)
			continue
		}
		if !tc.bad && !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseMode(t *testing.T) {
	cases := []struct {
		spec string
		cur  os.FileMode
		want os.FileMode
	}{
		{"644", 0, 0o644},
		{"0755", 0, 0o755},
		{"u+x", 0o644, 0o744},
		{"go-w", 0o666, 0o644},
		{"a=r", 0o755, 0o444},
		{"u+rwx,g+rx,o-rwx", 0o600, 0o750},
		{"+x", 0o644, 0o755},
	}
	for _, tc := range cases {
		got, err := parseMode(tc.spec, tc.cur)
		if err != nil || got.Perm() != tc.want {
			t.Errorf("parseMode(%q, %o) = %o, %v; want %o", tc.spec, tc.cur, got.Perm(), err, tc.want)
		}
	}
	for _, bad := range []string{"", "u+q", "999", "u", "z+x"} {
		if _, err := parseMode(bad, 0o644); err == nil {
			t.Errorf("parseMode(%q) should fail", bad)
		}
	}
	if p, ok := currentPerm("-rw-r--r--"); !ok || p != 0o644 {
		t.Errorf("currentPerm = %o, %v", p, ok)
	}
	if p, ok := currentPerm("drwxr-xr-x"); !ok || p != 0o755 {
		t.Errorf("currentPerm dir = %o, %v", p, ok)
	}
	if _, ok := currentPerm("file"); ok {
		t.Errorf("FTP-style mode should not parse")
	}
}

func TestCatStreamsToStdout(t *testing.T) {
	code, out, errOut := run(t, fakefs.NewRemote(), "cat", "/welcome.txt")
	if code != 0 || out != "Welcome to the fake server.\n" {
		t.Fatalf("exit=%d out=%q err=%s", code, out, errOut)
	}
	if code, _, _ := run(t, fakefs.NewRemote(), "cat", "/incoming"); code != 2 {
		t.Fatalf("cat on a directory = %d, want 2", code)
	}
}

func seedTree(t *testing.T) *fakefs.Remote {
	t.Helper()
	r := fakefs.NewRemote()
	ctx := context.Background()
	_ = r.Mkdir(ctx, "/incoming/t")
	_ = r.Mkdir(ctx, "/incoming/t/sub")
	_ = r.WriteFile(ctx, "/incoming/t/a.txt", []byte("12345"))
	_ = r.WriteFile(ctx, "/incoming/t/b.log", []byte("1234567890"))
	_ = r.WriteFile(ctx, "/incoming/t/sub/c.txt", []byte("123"))
	return r
}

func TestDu(t *testing.T) {
	r := seedTree(t)
	code, out, errOut := run(t, r, "du", "/incoming/t")
	if code != 0 || strings.TrimSpace(out) != "18\t/incoming/t" {
		t.Fatalf("exit=%d out=%q err=%s", code, out, errOut)
	}
	_, out, _ = run(t, r, "du", "-d", "1", "/incoming/t")
	if !strings.Contains(out, "3\t/incoming/t/sub") || !strings.Contains(out, "18\t/incoming/t") {
		t.Fatalf("du -d 1 = %q", out)
	}
	_, out, _ = run(t, r, "du", "-h", "/incoming/t")
	if !strings.HasPrefix(out, "18 B") {
		t.Fatalf("du -h = %q", out)
	}
}

func TestFind(t *testing.T) {
	r := seedTree(t)
	_, out, _ := run(t, r, "find", "--name", "*.txt", "/incoming/t")
	if out != "/incoming/t/a.txt\n/incoming/t/sub/c.txt\n" {
		t.Fatalf("find --name = %q", out)
	}
	_, out, _ = run(t, r, "find", "--type", "d", "/incoming/t")
	if out != "/incoming/t/sub\n" {
		t.Fatalf("find --type d = %q", out)
	}
	_, out, _ = run(t, r, "find", "--min-size", "6", "/incoming/t")
	if out != "/incoming/t/b.log\n" {
		t.Fatalf("find --min-size = %q", out)
	}
	_, out, _ = run(t, r, "find", "--maxdepth", "1", "/incoming/t")
	if strings.Contains(out, "c.txt") {
		t.Fatalf("find --maxdepth 1 descended: %q", out)
	}
	if code, _, _ := run(t, r, "find", "--type", "x", "/incoming/t"); code != 2 {
		t.Fatalf("bad --type = %d, want 2", code)
	}
}

func TestTree(t *testing.T) {
	_, out, _ := run(t, seedTree(t), "tree", "/incoming/t")
	want := "/incoming/t\n├── sub/\n│   └── c.txt\n├── a.txt\n└── b.log\n\n1 directories, 3 files\n"
	if out != want {
		t.Fatalf("tree =\n%s\nwant\n%s", out, want)
	}
	_, out, _ = run(t, seedTree(t), "tree", "-L", "1", "/incoming/t")
	if strings.Contains(out, "c.txt") {
		t.Fatalf("tree -L 1 descended: %q", out)
	}
}

func TestChmod(t *testing.T) {
	r := seedTree(t)
	if code, out, errOut := run(t, r, "chmod", "600", "/incoming/t/a.txt"); code != 0 || !strings.Contains(out, "0600") {
		t.Fatalf("exit=%d out=%q err=%s", code, out, errOut)
	}
	e, _ := r.Stat(context.Background(), "/incoming/t/a.txt")
	if !strings.Contains(e.Mode, "rw-------") {
		t.Fatalf("mode = %q, want rw-------", e.Mode)
	}
	if code, _, _ := run(t, r, "chmod", "-R", "755", "/incoming/t"); code != 0 {
		t.Fatalf("chmod -R failed")
	}
	e, _ = r.Stat(context.Background(), "/incoming/t/sub/c.txt")
	if !strings.Contains(e.Mode, "rwxr-xr-x") {
		t.Fatalf("recursive mode = %q", e.Mode)
	}
	if code, _, _ := run(t, r, "chmod", "bogus", "/incoming/t/a.txt"); code != 2 {
		t.Fatalf("bad mode = %d, want 2", code)
	}
}

// countingApp is testApp with a dial counter, to prove a script connects once.
func countingApp(t *testing.T, remote *fakefs.Remote) (*App, *int32, *strings.Builder, *strings.Builder) {
	t.Helper()
	app, _, _ := testApp(t, remote)
	var out, errOut strings.Builder
	app.Stdout, app.Stderr = &out, &errOut
	var dials int32
	inner := app.Dial
	app.Dial = func(o connect.Options) (session.Dialer, error) {
		d, err := inner(o)
		return countDialer{inner: d, n: &dials}, err
	}
	return app, &dials, &out, &errOut
}

type countDialer struct {
	inner session.Dialer
	n     *int32
}

func (d countDialer) Dial(ctx context.Context, t session.Target, c session.Credentials) (session.Conn, error) {
	atomic.AddInt32(d.n, 1)
	return d.inner.Dial(ctx, t, c)
}

func TestScriptRunsManyCommandsOverOneConnection(t *testing.T) {
	remote := fakefs.NewRemote()
	local := t.TempDir()
	writeLocal(t, local, "up.txt", "payload")
	app, dials, out, errOut := countingApp(t, remote)

	script := "mkdir /incoming/s\ncd /incoming/s\nlcd " + local + "\nput up.txt\n# comment\nls; pwd\nmv up.txt renamed.txt\nexists -f renamed.txt\n"
	code := app.Run([]string{"script", "--host", "x", "-c", script})
	if code != 0 {
		t.Fatalf("exit = %d\nstderr=%s", code, errOut.String())
	}
	if *dials != 1 {
		t.Fatalf("dialled %d times, want 1", *dials)
	}
	if got := readRemote(t, remote, "/incoming/s/renamed.txt"); got != "payload" {
		t.Fatalf("remote file = %q", got)
	}
	if !strings.Contains(out.String(), "/incoming/s\n") {
		t.Fatalf("pwd missing from output: %q", out.String())
	}
}

func TestScriptStopsAtFirstErrorByDefault(t *testing.T) {
	remote := fakefs.NewRemote()
	app, _, _, errOut := countingApp(t, remote)
	code := app.Run([]string{"script", "--host", "x", "-c", "mkdir /incoming/one; get /nope.txt /tmp/x-nope; mkdir /incoming/two"})
	if code != 5 {
		t.Fatalf("exit = %d, want 5 (not found); stderr=%s", code, errOut.String())
	}
	if c, _, _ := run(t, remote, "exists", "/incoming/one"); c != 0 {
		t.Fatalf("command before the failure did not run")
	}
	if c, _, _ := run(t, remote, "exists", "/incoming/two"); c != 5 {
		t.Fatalf("script continued past a failure")
	}
}

func TestScriptKeepGoingReportsFirstFailure(t *testing.T) {
	remote := fakefs.NewRemote()
	app, _, _, _ := countingApp(t, remote)
	code := app.Run([]string{"script", "--host", "x", "-k", "-c", "get /nope.txt /tmp/x-nope; mkdir /incoming/two; frobnicate"})
	if code != 5 {
		t.Fatalf("exit = %d, want the first failure's 5", code)
	}
	if c, _, _ := run(t, remote, "exists", "/incoming/two"); c != 0 {
		t.Fatalf("-k did not continue")
	}
}

func TestScriptExitAndFile(t *testing.T) {
	remote := fakefs.NewRemote()
	dir := t.TempDir()
	file := filepath.Join(dir, "job.tide")
	_ = os.WriteFile(file, []byte("mkdir /incoming/k\nexit 7\nmkdir /incoming/after\n"), 0o644)
	app, _, _, _ := countingApp(t, remote)
	if code := app.Run([]string{"script", "--host", "x", file}); code != 7 {
		t.Fatalf("exit = %d, want 7", code)
	}
	if c, _, _ := run(t, remote, "exists", "/incoming/after"); c != 5 {
		t.Fatalf("commands after exit ran")
	}
}

func TestScriptCdChecksDirectory(t *testing.T) {
	app, _, _, errOut := countingApp(t, fakefs.NewRemote())
	if code := app.Run([]string{"script", "--host", "x", "-c", "cd /welcome.txt"}); code != 2 {
		t.Fatalf("cd to a file = %d, want 2; %s", code, errOut.String())
	}
	if code := app.Run([]string{"script", "--host", "x", "-c", "cd /missing"}); code != 5 {
		t.Fatalf("cd to a missing dir = %d, want 5", code)
	}
}

func TestShellReadsStdinAndKeepsGoing(t *testing.T) {
	remote := fakefs.NewRemote()
	app, _, out, errOut := countingApp(t, remote)
	app.Stdin = strings.NewReader("pwd\nbogus\nmkdir /incoming/sh\nexit\n")
	code := app.Run([]string{"shell", "--host", "x"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2 from the unknown command; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "tideftp /> ") {
		t.Fatalf("no prompt on stderr: %q", errOut.String())
	}
	if !strings.Contains(out.String(), "/\n") {
		t.Fatalf("pwd output missing: %q", out.String())
	}
	if c, _, _ := run(t, remote, "exists", "/incoming/sh"); c != 0 {
		t.Fatalf("shell stopped at the error")
	}
}

func TestScriptRejectsPasswordStdinWithStdinScript(t *testing.T) {
	app, _, _, _ := countingApp(t, fakefs.NewRemote())
	app.Stdin = strings.NewReader("pw\n")
	if code := app.Run([]string{"script", "--host", "x", "--password-stdin"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestMirrorIsSyncAlias(t *testing.T) {
	remote := fakefs.NewRemote()
	src := t.TempDir()
	writeLocal(t, src, "a.txt", "A")
	if code, _, e := syncRunCLI(t, hosts(remote), "--dry-run", src, "alpha:/incoming/m"); code != 0 {
		t.Fatalf("sync: %s", e)
	}
	app, _, _ := syncApp(t, hosts(remote))
	if code := app.Run([]string{"mirror", src, "alpha:/incoming/m"}); code != 0 {
		t.Fatalf("mirror exit = %d", code)
	}
	if readRemote(t, remote, "/incoming/m/a.txt") != "A" {
		t.Fatalf("mirror did not copy")
	}
}
