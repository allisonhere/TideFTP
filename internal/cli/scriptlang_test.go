package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tideftp/internal/connect"
	"tideftp/internal/fakefs"
	"tideftp/internal/session"
)

// scriptApp is an App over a locking, per-dial fake server (profile "alpha"),
// safe for background jobs, with a dial counter.
func scriptApp(t *testing.T, remote *fakefs.Remote) (*App, *bytes.Buffer, *bytes.Buffer, *int32) {
	t.Helper()
	app, out, errOut := syncApp(t, hosts(remote))
	app.Sleep = func(time.Duration) {}
	var dials int32
	inner := app.Dial
	app.Dial = func(o connect.Options) (session.Dialer, error) {
		d, err := inner(o)
		return countDialer{inner: d, n: &dials}, err
	}
	return app, out, errOut, &dials
}

func runScript(t *testing.T, remote *fakefs.Remote, script string, extra ...string) (int, string, string) {
	t.Helper()
	app, out, errOut, _ := scriptApp(t, remote)
	args := append([]string{"script", "--profile", "alpha"}, extra...)
	args = append(args, "-c", script)
	code := app.Run(args)
	return code, out.String(), errOut.String()
}

func TestScriptSetAndAlias(t *testing.T) {
	remote := fakefsRemote()
	local := t.TempDir()
	writeLocal(t, local, "a.txt", "A")

	code, out, e := runScript(t, remote, "set net:limit-rate 1M\nset net:max-retries 2\nset net:reconnect-interval-base 0.5\nset net:limit-rate\n")
	if code != 0 || !strings.Contains(out, "net:limit-rate 1M") {
		t.Fatalf("set: exit %d out=%q err=%s", code, out, e)
	}
	if code, _, _ := runScript(t, remote, "set net:nonsense 1"); code != 2 {
		t.Fatalf("unknown setting = %d, want 2", code)
	}
	if code, _, _ := runScript(t, remote, "set net:limit-rate fast"); code != 2 {
		t.Fatalf("bad value = %d, want 2", code)
	}
	if code, out, _ := runScript(t, remote, "set"); code != 0 || !strings.Contains(out, "mirror:parallel") {
		t.Fatalf("set with no args should list settings: %q", out)
	}

	script := "lcd " + local + "\nalias upload put --force\nupload a.txt /incoming/a.txt\nalias\nalias upload\nalias\n"
	code, out, e = runScript(t, remote, script)
	if code != 0 {
		t.Fatalf("alias: exit %d err=%s", code, e)
	}
	if readRemote(t, remote, "/incoming/a.txt") != "A" {
		t.Fatalf("alias did not expand")
	}
	if strings.Count(out, "alias upload put --force") != 1 {
		t.Fatalf("alias listing wrong: %q", out)
	}
}

func TestScriptFailExitSetting(t *testing.T) {
	remote := fakefsRemote()
	code, _, _ := runScript(t, remote, "set cmd:fail-exit no\nget /nope /tmp/x-nope\nmkdir /incoming/after\n")
	if code != 5 {
		t.Fatalf("exit = %d, want the first failure's 5", code)
	}
	if c, _, _ := run(t, remote, "exists", "/incoming/after"); c != 0 {
		t.Fatalf("cmd:fail-exit no did not keep going")
	}
}

func TestScriptSourceEchoAndShell(t *testing.T) {
	remote := fakefsRemote()
	dir := t.TempDir()
	inc := filepath.Join(dir, "inc.tide")
	_ = os.WriteFile(inc, []byte("echo from-include\nmkdir /incoming/sourced\n"), 0o644)
	marker := filepath.Join(dir, "shell.out")

	script := "source " + inc + "\necho -n 'no newline'\necho\n!echo shelled > " + marker + "; echo and-this\n"
	code, out, e := runScript(t, remote, script)
	if code != 0 {
		t.Fatalf("exit %d err=%s", code, e)
	}
	if !strings.Contains(out, "from-include\n") || !strings.Contains(out, "no newline\n") {
		t.Fatalf("echo output wrong: %q", out)
	}
	if c, _, _ := run(t, remote, "exists", "/incoming/sourced"); c != 0 {
		t.Fatalf("source did not run its commands")
	}
	if b, _ := os.ReadFile(marker); strings.TrimSpace(string(b)) != "shelled" {
		t.Fatalf("! did not run in the local shell: %q", b)
	}
	if !strings.Contains(out, "and-this") {
		t.Fatalf("`!` should take the whole rest of the line, ; included")
	}
	if code, _, _ := runScript(t, remote, "!exit 3"); code == 0 {
		t.Fatalf("a failing ! command should fail the script")
	}
}

func TestScriptOpenAndClose(t *testing.T) {
	remote := fakefsRemote()
	app, _, errOut := syncApp(t, hosts(remote))
	app.Sleep = func(time.Duration) {}
	// No --host: the script starts unconnected and opens a saved profile.
	code := app.Run([]string{"script", "-c", "open alpha\ncd /incoming\nmkdir opened\nclose\nmkdir nope"})
	if code != 2 {
		t.Fatalf("a command after close should be a usage error (exit %d): %s", code, errOut.String())
	}
	if c, _, _ := run(t, remote, "exists", "/incoming/opened"); c != 0 {
		t.Fatalf("open + commands did not work")
	}
	// Commands before open are refused.
	app2, _, _ := syncApp(t, hosts(remote))
	if code := app2.Run([]string{"script", "-c", "ls /"}); code != 2 {
		t.Fatalf("ls before open = %d, want 2", code)
	}
	// open can switch servers mid-script.
	other := fakefsRemote()
	app3, _, _ := syncApp(t, map[string]*fakefs.Remote{"alpha": remote, "beta": other})
	if code := app3.Run([]string{"script", "-c", "open alpha\nmkdir /incoming/on-alpha\nopen beta\nmkdir /incoming/on-beta"}); code != 0 {
		t.Fatalf("switching servers failed: %d", code)
	}
	if c, _, _ := run(t, other, "exists", "/incoming/on-beta"); c != 0 {
		t.Fatalf("second open did not reach beta")
	}
	if c, _, _ := run(t, other, "exists", "/incoming/on-alpha"); c != 5 {
		t.Fatalf("command leaked to the wrong server")
	}
}

func TestScriptBackgroundJobsAndWait(t *testing.T) {
	remote := fakefsRemote()
	local := t.TempDir()
	writeLocal(t, local, "bg1.txt", "one")
	writeLocal(t, local, "bg2.txt", "two")
	app, out, errOut, dials := scriptApp(t, remote)
	script := "lcd " + local + "\nput bg1.txt /incoming/bg1.txt &\nput bg2.txt /incoming/bg2.txt &\nwait\njobs\n"
	if code := app.Run([]string{"script", "--profile", "alpha", "-c", script}); code != 0 {
		t.Fatalf("exit %d\nstderr=%s", code, errOut.String())
	}
	if readRemote(t, remote, "/incoming/bg1.txt") != "one" || readRemote(t, remote, "/incoming/bg2.txt") != "two" {
		t.Fatalf("background puts did not complete")
	}
	if strings.Contains(out.String(), "Running") {
		t.Fatalf("`jobs` after `wait` should list nothing: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "Done: put bg1.txt") {
		t.Fatalf("no completion notice: %q", errOut.String())
	}
	if *dials != 3 {
		t.Fatalf("dialled %d times, want 1 (script) + 2 (one per job)", *dials)
	}
}

func TestScriptBackgroundFailureSetsExitCode(t *testing.T) {
	remote := fakefsRemote()
	code, _, errOut := runScript(t, remote, "get /nope /tmp/x-bg-nope &\nwait\n")
	if code != 5 {
		t.Fatalf("exit = %d, want 5 from the failed job; %s", code, errOut)
	}
	if !strings.Contains(errOut, "Failed: get /nope") {
		t.Fatalf("no failure notice: %q", errOut)
	}
}

func TestScriptEndWaitsForJobs(t *testing.T) {
	remote := fakefsRemote()
	local := t.TempDir()
	writeLocal(t, local, "late.txt", "late")
	// No explicit `wait`: finishing the script must still let the job complete.
	code, _, e := runScript(t, remote, "lcd "+local+"\nput late.txt /incoming/late.txt &\n")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	if readRemote(t, remote, "/incoming/late.txt") != "late" {
		t.Fatalf("the script exited before its background job finished")
	}
}

func TestScriptQueueRunsInOrder(t *testing.T) {
	remote := fakefsRemote()
	local := t.TempDir()
	writeLocal(t, local, "q1.txt", "1")
	writeLocal(t, local, "q2.txt", "2")
	app, _, errOut, dials := scriptApp(t, remote)
	script := "lcd " + local + "\nqueue mkdir /incoming/qd\nqueue put q1.txt /incoming/qd/q1.txt\nqueue put q2.txt /incoming/qd/q2.txt\nwait\n"
	if code := app.Run([]string{"script", "--profile", "alpha", "-c", script}); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if readRemote(t, remote, "/incoming/qd/q1.txt") != "1" || readRemote(t, remote, "/incoming/qd/q2.txt") != "2" {
		t.Fatalf("queue did not run everything in order")
	}
	if *dials != 2 {
		t.Fatalf("dialled %d times, want the script's plus ONE for the whole queue", *dials)
	}
}

func TestScriptRCFile(t *testing.T) {
	remote := fakefsRemote()
	app, out, errOut, _ := scriptApp(t, remote)
	cfgDir := filepath.Dir(app.ConfigPath)
	_ = os.WriteFile(filepath.Join(cfgDir, "cli.rc"), []byte("# defaults\nalias mk mkdir\nset net:max-retries 1\n"), 0o644)
	if code := app.Run([]string{"script", "--profile", "alpha", "-c", "mk /incoming/from-rc\nset net:max-retries"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if c, _, _ := run(t, remote, "exists", "/incoming/from-rc"); c != 0 {
		t.Fatalf("the rc file's alias was not loaded")
	}
	if !strings.Contains(out.String(), "net:max-retries 1") {
		t.Fatalf("the rc file's set was not applied: %q", out.String())
	}

	// Only set/alias/echo/source are allowed in an rc file.
	_ = os.WriteFile(filepath.Join(cfgDir, "cli.rc"), []byte("mkdir /incoming/sneaky\n"), 0o644)
	app2, _, _, _ := scriptApp(t, remote)
	app2.ConfigPath = app.ConfigPath
	if code := app2.Run([]string{"script", "--profile", "alpha", "-c", "pwd"}); code == 0 {
		t.Fatalf("an rc file that runs mkdir should be refused")
	}
	app3, _, _, _ := scriptApp(t, remote)
	app3.ConfigPath = app.ConfigPath
	if code := app3.Run([]string{"script", "--profile", "alpha", "--no-rc", "-c", "pwd"}); code != 0 {
		t.Fatalf("--no-rc should skip the rc file (exit %d)", code)
	}
}

func TestHelpForACommand(t *testing.T) {
	app, _, errOut, _ := scriptApp(t, fakefsRemote())
	if code := app.Run([]string{"help", "sync"}); code != 0 || !strings.Contains(errOut.String(), "usage: tideftp sync") {
		t.Fatalf("help sync: exit %d out=%q", code, errOut.String())
	}
	if code := app.Run([]string{"help", "nonsense"}); code != 2 {
		t.Fatalf("help nonsense = %d, want 2", code)
	}
	if code, _, e := runScript(t, fakefsRemote(), "help mirror"); code != 0 || !strings.Contains(e, "usage: tideftp mirror") {
		t.Fatalf("help inside a script: exit %d err=%q", code, e)
	}
}

func TestScriptExitKillStopsJobs(t *testing.T) {
	remote := fakefsRemote()
	code, _, e := runScript(t, remote, "mkdir /incoming/k1 &\nexit kill\n")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
}

func TestParseScriptBackground(t *testing.T) {
	cmds, err := parseScript(`put a b & ls; echo "a & b" &`)
	if err != nil || len(cmds) != 3 {
		t.Fatalf("got %+v, %v", cmds, err)
	}
	if !cmds[0].bg || cmds[1].bg || !cmds[2].bg || cmds[2].words[1] != "a & b" {
		t.Fatalf("background flags wrong: %+v", cmds)
	}
	if _, err := parseScript("ls && pwd"); err == nil {
		t.Fatalf("&& should be rejected clearly")
	}
	if _, err := parseScript("& ls"); err == nil {
		t.Fatalf("a leading & should be rejected")
	}
}
