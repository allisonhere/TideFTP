package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tideftp/internal/connect"
	"tideftp/internal/fakefs"
	"tideftp/internal/session"
)

type failingDialer struct {
	err   error
	calls *int
}

func (d failingDialer) Dial(context.Context, session.Target, session.Credentials) (session.Conn, error) {
	*d.calls++
	return nil, d.err
}

func appWithDialErr(t *testing.T, err error) (*App, *int, *[]time.Duration) {
	t.Helper()
	app, _, _ := testApp(t, fakefs.NewRemote())
	calls := 0
	var sleeps []time.Duration
	app.Dial = func(connect.Options) (session.Dialer, error) {
		return failingDialer{err: err, calls: &calls}, nil
	}
	app.Sleep = func(d time.Duration) { sleeps = append(sleeps, d) }
	return app, &calls, &sleeps
}

func TestLsJSON(t *testing.T) {
	code, out, errOut := run(t, fakefs.NewRemote(), "ls", "--json", "/")
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	var rows []entryJSON
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	found := false
	for _, r := range rows {
		if r.Name == "incoming" {
			found = true
			if r.Type != "dir" || r.Path != "/incoming" {
				t.Errorf("incoming = %+v", r)
			}
		}
	}
	if !found {
		t.Fatalf("no incoming entry in %+v", rows)
	}
}

func TestLsJSONEmptyIsAnArray(t *testing.T) {
	remote := fakefs.NewRemote()
	code, out, _ := run(t, remote, "ls", "--json", "/incoming")
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Fatalf("exit=%d out=%q, want a JSON array", code, out)
	}
}

func TestStatAndExists(t *testing.T) {
	remote := fakefs.NewRemote()
	if code, out, _ := run(t, remote, "stat", "--json", "/welcome.txt"); code != 0 || !strings.Contains(out, `"type": "file"`) {
		t.Fatalf("stat exit=%d out=%s", code, out)
	}
	if code, _, _ := run(t, remote, "exists", "/welcome.txt"); code != 0 {
		t.Fatalf("exists file = %d, want 0", code)
	}
	if code, out, errOut := run(t, remote, "exists", "/nope"); code != 5 || out != "" || errOut != "" {
		t.Fatalf("exists missing = %d out=%q err=%q, want silent 5", code, out, errOut)
	}
	if code, _, _ := run(t, remote, "exists", "-d", "/welcome.txt"); code != 5 {
		t.Fatalf("exists -d on a file = %d, want 5", code)
	}
	if code, _, _ := run(t, remote, "exists", "-d", "/incoming"); code != 0 {
		t.Fatalf("exists -d on a dir = %d, want 0", code)
	}
	if code, _, _ := run(t, remote, "stat", "/nope"); code != 5 {
		t.Fatalf("stat missing = %d, want 5", code)
	}
}

func TestNotFoundExitsFive(t *testing.T) {
	dir := t.TempDir()
	if code, _, _ := run(t, fakefs.NewRemote(), "get", "/nope.txt", filepath.Join(dir, "x")); code != 5 {
		t.Fatalf("get missing = %d, want 5", code)
	}
}

func TestGetGlobIntoDirectory(t *testing.T) {
	remote := fakefs.NewRemote()
	dir := t.TempDir()
	code, _, errOut := run(t, remote, "get", "/public_html/*.txt", dir)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "robots.txt")); err != nil {
		t.Fatalf("robots.txt not downloaded: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "app.css")); err == nil {
		t.Fatalf("*.txt should not match app.css")
	}
}

func TestGlobWithNoMatchIsNotFound(t *testing.T) {
	if code, _, _ := run(t, fakefs.NewRemote(), "get", "/public_html/*.zip", t.TempDir()); code != 5 {
		t.Fatalf("exit = %d, want 5", code)
	}
	if code, _, _ := run(t, fakefs.NewRemote(), "rm", "/public_html/*.zip"); code != 5 {
		t.Fatalf("rm exit = %d, want 5", code)
	}
}

func TestGetSeveralSourcesNeedsDestination(t *testing.T) {
	dir := t.TempDir()
	code, _, errOut := run(t, fakefs.NewRemote(), "get", "/welcome.txt", "/public_html/robots.txt", dir)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	for _, name := range []string{"welcome.txt", "robots.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s missing: %v", name, err)
		}
	}
}

func TestRmGlob(t *testing.T) {
	remote := fakefs.NewRemote()
	if code, _, errOut := run(t, remote, "rm", "/public_html/*.txt"); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	if code, _, _ := run(t, remote, "exists", "/public_html/robots.txt"); code != 5 {
		t.Fatalf("robots.txt survived rm *.txt")
	}
	if code, _, _ := run(t, remote, "exists", "/public_html/app.css"); code != 0 {
		t.Fatalf("app.css should not have been removed")
	}
}

func TestPutSeveralSources(t *testing.T) {
	remote := fakefs.NewRemote()
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	_ = os.WriteFile(a, []byte("A"), 0o644)
	_ = os.WriteFile(b, []byte("B"), 0o644)
	if code, _, errOut := run(t, remote, "put", a, b, "/incoming"); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	for name, want := range map[string]string{"/incoming/a.txt": "A", "/incoming/b.txt": "B"} {
		got, err := remote.ReadFile(context.Background(), name)
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
}

func TestPutLocalGlobExpandedByTool(t *testing.T) {
	remote := fakefs.NewRemote()
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.log"), []byte("A"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.log"), []byte("B"), 0o644)
	if code, _, errOut := run(t, remote, "put", filepath.Join(dir, "*.log"), "/incoming"); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	if code, _, _ := run(t, remote, "exists", "/incoming/a.log", "/incoming/b.log"); code != 0 {
		t.Fatalf("globbed files not uploaded")
	}
}

func TestPutLeavesNoPartFile(t *testing.T) {
	remote := fakefs.NewRemote()
	src := filepath.Join(t.TempDir(), "f.txt")
	_ = os.WriteFile(src, []byte("x"), 0o644)
	if code, _, errOut := run(t, remote, "put", src, "/incoming/f.txt"); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	if code, _, _ := run(t, remote, "exists", "/incoming/f.txt.part"); code != 5 {
		t.Fatalf(".part file left behind")
	}
}

func TestPutForceReplacesExisting(t *testing.T) {
	remote := fakefs.NewRemote()
	src := filepath.Join(t.TempDir(), "robots.txt")
	_ = os.WriteFile(src, []byte("new"), 0o644)
	if code, _, errOut := run(t, remote, "put", "--force", src, "/public_html/robots.txt"); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut)
	}
	got, _ := remote.ReadFile(context.Background(), "/public_html/robots.txt")
	if string(got) != "new" {
		t.Fatalf("body = %q, want new", got)
	}
}

func TestDialFailureExitCodes(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{errors.New("dial tcp: connection refused"), 3},
		{errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password]"), 4},
		{errors.New("530 Login incorrect."), 4},
	}
	for _, tc := range cases {
		app, _, _ := appWithDialErr(t, tc.err)
		if got := app.Run([]string{"ls", "--host", "x"}); got != tc.want {
			t.Errorf("%v: exit = %d, want %d", tc.err, got, tc.want)
		}
	}
}

func TestRetriesOnlyConnectionErrors(t *testing.T) {
	app, calls, sleeps := appWithDialErr(t, errors.New("dial tcp: connection refused"))
	if got := app.Run([]string{"ls", "--host", "x", "--retries", "2"}); got != 3 {
		t.Fatalf("exit = %d, want 3", got)
	}
	if *calls != 3 {
		t.Fatalf("dialed %d times, want 3", *calls)
	}
	if len(*sleeps) != 2 || (*sleeps)[0] != time.Second || (*sleeps)[1] != 2*time.Second {
		t.Fatalf("backoff = %v, want [1s 2s]", *sleeps)
	}

	app, calls, _ = appWithDialErr(t, errors.New("ssh: unable to authenticate"))
	if got := app.Run([]string{"ls", "--host", "x", "--retries", "5"}); got != 4 || *calls != 1 {
		t.Fatalf("auth failure: exit=%d calls=%d, want 4 with no retry", got, *calls)
	}
}

func TestPasswordStdin(t *testing.T) {
	var got session.Credentials
	app, _, _ := testApp(t, fakefs.NewRemote())
	app.Stdin = strings.NewReader("s3cret\r\nignored\n")
	inner := app.Dial
	app.Dial = func(o connect.Options) (session.Dialer, error) {
		d, err := inner(o)
		return captureDialer{inner: d, got: &got}, err
	}
	if code := app.Run([]string{"ls", "--host", "x", "--password-stdin", "/"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if got.Password != "s3cret" || !got.PasswordOnly {
		t.Fatalf("credentials = %+v, want password s3cret with PasswordOnly for sftp", got)
	}
}

func TestPasswordStdinEmptyIsUsageError(t *testing.T) {
	app, _, _ := testApp(t, fakefs.NewRemote())
	app.Stdin = strings.NewReader("")
	if code := app.Run([]string{"ls", "--host", "x", "--password-stdin"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

type captureDialer struct {
	inner session.Dialer
	got   *session.Credentials
}

func (d captureDialer) Dial(ctx context.Context, t session.Target, c session.Credentials) (session.Conn, error) {
	*d.got = c
	return d.inner.Dial(ctx, t, c)
}
