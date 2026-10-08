package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tideftp/internal/connect"
	"tideftp/internal/testserver"
)

// TestMain pins the process to UTC. goftp's test server formats MDTM/MLSD
// timestamps in local time rather than UTC (RFC 3659 says UTC), which on a
// machine outside UTC makes every FTP file look hours old and sync re-upload
// it forever. Real servers send UTC; this only removes the test server's quirk.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}

// e2eEnv is one real protocol server plus the flags that reach it. Everything
// in this file talks genuine SFTP or FTP over loopback — the same adapters and
// transfer engines the shipped binary uses — rather than the fakefs the rest
// of the CLI tests run on.
type e2eEnv struct {
	name    string
	flags   []string
	root    string // remote path of the server's root
	disk    func(rel string) string
	write   func(t *testing.T, rel string, body []byte)
	drop    func() // sever live connections, keep listening
	protoNm string
}

func (e *e2eEnv) rp(rel string) string { return strings.TrimSuffix(e.root, "/") + "/" + rel }

func e2eEnvs(t *testing.T) []*e2eEnv {
	t.Helper()
	sftpSrv := testserver.StartSFTP(t)
	ftpSrv := testserver.StartFTP(t)
	t.Setenv("TIDEFTP_FTP_PASSWORD", testserver.FTPPass)
	return []*e2eEnv{
		{
			name: "sftp", protoNm: "sftp", root: sftpSrv.Root,
			flags: []string{"--protocol", "sftp", "--host", sftpSrv.Host(), "--port", sftpSrv.Port(),
				"--user", "tester", "--identity", sftpSrv.IdentityFile(), "--known-hosts", sftpSrv.KnownHosts(t)},
			disk:  sftpSrv.Path,
			write: func(t *testing.T, rel string, b []byte) { sftpSrv.WriteFile(t, rel, b) },
			drop:  sftpSrv.DropConnections,
		},
		{
			name: "ftp", protoNm: "ftp", root: "/",
			flags: []string{"--protocol", "ftp", "--host", ftpSrv.Host(), "--port", ftpSrv.Port(),
				"--user", testserver.FTPUser},
			disk:  ftpSrv.Path,
			write: func(t *testing.T, rel string, b []byte) { ftpSrv.WriteFile(t, rel, b) },
			drop:  ftpSrv.DropConnections,
		},
	}
}

// forEachProtocol runs fn once per real protocol.
func forEachProtocol(t *testing.T, fn func(t *testing.T, e *e2eEnv)) {
	t.Helper()
	for _, e := range e2eEnvs(t) {
		e := e
		t.Run(e.name, func(t *testing.T) { fn(t, e) })
	}
}

// do runs `tideftp CMD <connection flags> ARGS...` against the real dialer.
func (e *e2eEnv) do(t *testing.T, cmd string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	app := App{
		Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut,
		Dial:       connect.Dialer,
		ConfigPath: filepath.Join(t.TempDir(), "none.toml"),
	}
	full := append([]string{cmd}, e.flags...)
	full = append(full, args...)
	code := app.Run(full)
	return code, out.String(), errOut.String()
}

func (e *e2eEnv) mustDo(t *testing.T, cmd string, args ...string) string {
	t.Helper()
	code, out, errOut := e.do(t, cmd, args...)
	if code != 0 {
		t.Fatalf("%s %v: exit %d\nstdout=%s\nstderr=%s", cmd, args, code, out, errOut)
	}
	return out
}

func (e *e2eEnv) read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(e.disk(rel))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestE2EListStatCat(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e *e2eEnv) {
		e.write(t, "docs/a.txt", []byte("alpha"))
		e.write(t, "docs/b.log", []byte("beta!"))

		var rows []entryJSON
		if err := json.Unmarshal([]byte(e.mustDo(t, "ls", "--json", e.rp("docs"))), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 {
			t.Fatalf("ls --json = %+v, want 2 rows", rows)
		}
		if out := e.mustDo(t, "cat", e.rp("docs/a.txt")); out != "alpha" {
			t.Fatalf("cat = %q", out)
		}
		if !strings.Contains(e.mustDo(t, "stat", "--json", e.rp("docs/a.txt")), `"size": 5`) {
			t.Fatalf("stat missing size 5")
		}
		if code, _, _ := e.do(t, "exists", "-f", e.rp("docs/a.txt")); code != 0 {
			t.Fatalf("exists = %d", code)
		}
		if code, _, _ := e.do(t, "exists", e.rp("docs/nope")); code != 5 {
			t.Fatalf("exists missing = %d, want 5", code)
		}
		if out := e.mustDo(t, "ls", e.rp("docs/*.txt")); strings.TrimSpace(out) != "a.txt" {
			t.Fatalf("glob ls = %q", out)
		}
	})
}

func TestE2EPutGetRoundTrip(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e *e2eEnv) {
		dir := t.TempDir()
		big := make([]byte, 3<<20+123) // several engine chunks
		_, _ = rand.Read(big)
		src := writeLocal(t, dir, "big.bin", string(big))

		e.mustDo(t, "put", src, e.rp("big.bin"))
		if got := e.read(t, "big.bin"); got != string(big) {
			t.Fatalf("uploaded file differs (len %d vs %d)", len(got), len(big))
		}
		if e.read(t, "big.bin.part") != "<missing>" {
			t.Fatalf(".part left behind")
		}
		back := filepath.Join(dir, "back.bin")
		e.mustDo(t, "get", e.rp("big.bin"), back)
		if b, _ := os.ReadFile(back); string(b) != string(big) {
			t.Fatalf("downloaded file differs")
		}
		// An existing remote file is refused without --force.
		if code, _, _ := e.do(t, "put", src, e.rp("big.bin")); code != 1 {
			t.Fatalf("overwrite without --force = %d, want 1", code)
		}
		e.mustDo(t, "put", "--force", src, e.rp("big.bin"))
	})
}

func TestE2ETreeCommands(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e *e2eEnv) {
		src := t.TempDir()
		writeLocal(t, src, "top.txt", "top")
		writeLocal(t, src, "sub/deep.txt", "deep")

		e.mustDo(t, "mkdir", "-p", e.rp("up/inner"))
		e.mustDo(t, "put", "-r", src, e.rp("up/tree"))
		if e.read(t, "up/tree/sub/deep.txt") != "deep" {
			t.Fatalf("recursive upload missed a nested file")
		}
		dest := filepath.Join(t.TempDir(), "down")
		e.mustDo(t, "get", "-r", e.rp("up/tree"), dest)
		if b, _ := os.ReadFile(filepath.Join(dest, "sub", "deep.txt")); string(b) != "deep" {
			t.Fatalf("recursive download missed a nested file")
		}
		e.mustDo(t, "mv", e.rp("up/tree/top.txt"), e.rp("up/tree/moved.txt"))
		if e.read(t, "up/tree/moved.txt") != "top" || e.read(t, "up/tree/top.txt") != "<missing>" {
			t.Fatalf("mv did not move")
		}
		if !strings.Contains(e.mustDo(t, "tree", e.rp("up/tree")), "deep.txt") {
			t.Fatalf("tree missing a file")
		}
		if out := e.mustDo(t, "du", e.rp("up/tree")); !strings.HasPrefix(out, "7\t") {
			t.Fatalf("du = %q, want 7 bytes", out)
		}
		if out := e.mustDo(t, "find", "--name", "*.txt", e.rp("up/tree")); strings.Count(out, "\n") != 2 {
			t.Fatalf("find = %q", out)
		}
		e.mustDo(t, "rm", "-r", e.rp("up"))
		if code, _, _ := e.do(t, "exists", e.rp("up")); code != 5 {
			t.Fatalf("rm -r left the tree behind")
		}
	})
}

func TestE2ESync(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e *e2eEnv) {
		src := t.TempDir()
		writeLocal(t, src, "a.txt", "A")
		writeLocal(t, src, "sub/b.txt", "BB")
		dst := ":" + e.rp("mirror")

		// A dry run changes nothing.
		out := e.mustDo(t, "sync", "--dry-run", src, dst)
		if !strings.Contains(out, "copy") || e.read(t, "mirror/a.txt") != "<missing>" {
			t.Fatalf("dry run wrong: out=%q", out)
		}
		e.mustDo(t, "sync", src, dst)
		if e.read(t, "mirror/a.txt") != "A" || e.read(t, "mirror/sub/b.txt") != "BB" {
			t.Fatalf("sync did not copy")
		}
		// Second run: nothing to do.
		if out := e.mustDo(t, "sync", src, dst); strings.Contains(out, "copy") || strings.Contains(out, "update") {
			t.Fatalf("second sync not idempotent: %q", out)
		}
		// Changed size → update; extra remote file → deleted only with --delete.
		_ = os.WriteFile(filepath.Join(src, "a.txt"), []byte("A2"), 0o644)
		e.write(t, "mirror/extra.txt", []byte("x"))
		e.mustDo(t, "sync", src, dst)
		if e.read(t, "mirror/a.txt") != "A2" || e.read(t, "mirror/extra.txt") != "x" {
			t.Fatalf("update / keep-extra wrong")
		}
		e.mustDo(t, "sync", "--delete", src, dst)
		if e.read(t, "mirror/extra.txt") != "<missing>" {
			t.Fatalf("--delete left the extra file")
		}
		// And back down.
		back := t.TempDir()
		e.mustDo(t, "sync", dst, back)
		if b, _ := os.ReadFile(filepath.Join(back, "sub", "b.txt")); string(b) != "BB" {
			t.Fatalf("sync download missed a file")
		}
		e.mustDo(t, "sync", "--transfers", "3", "--checksum", src, dst)
	})
}

func TestE2EScript(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e *e2eEnv) {
		local := t.TempDir()
		writeLocal(t, local, "s.txt", "scripted")
		script := "mkdir -p " + e.rp("scr") + "\ncd " + e.rp("scr") + "\nlcd " + local + "\nput s.txt\nmv s.txt t.txt\npwd\n"
		out := e.mustDo(t, "script", "-c", script)
		if e.read(t, "scr/t.txt") != "scripted" {
			t.Fatalf("script did not complete; out=%s", out)
		}
	})
}

func TestE2EBandwidthLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("paced transfers take several seconds")
	}
	forEachProtocol(t, func(t *testing.T, e *e2eEnv) {
		body := make([]byte, 256<<10)
		_, _ = rand.Read(body)
		src := writeLocal(t, t.TempDir(), "limited.bin", string(body))

		// 256 KiB at 128 KiB/s: all but the final 32 KiB chunk is paced, so
		// at least ~1.7s. Unthrottled over loopback it is milliseconds.
		start := time.Now()
		e.mustDo(t, "put", "--bwlimit", "128k", src, e.rp("limited.bin"))
		up := time.Since(start)
		if up < 1400*time.Millisecond {
			t.Fatalf("upload took %v; --bwlimit 128k did not slow it", up)
		}
		if e.read(t, "limited.bin") != string(body) {
			t.Fatalf("limited upload corrupted the file")
		}

		start = time.Now()
		e.mustDo(t, "get", "--bwlimit", "128k", e.rp("limited.bin"), filepath.Join(t.TempDir(), "d.bin"))
		if down := time.Since(start); down < 1400*time.Millisecond {
			t.Fatalf("download took %v; --bwlimit 128k did not slow it", down)
		}

		start = time.Now()
		e.mustDo(t, "get", "--force", e.rp("limited.bin"), filepath.Join(t.TempDir(), "free.bin"))
		if free := time.Since(start); free > 1000*time.Millisecond {
			t.Fatalf("unlimited download took %v", free)
		}
	})
}

func TestBandwidthLimitRejectsBadValue(t *testing.T) {
	code, _, errOut := run(t, fakefsRemote(), "ls", "--bwlimit", "fast", "/")
	if code != 2 || !strings.Contains(errOut, "bwlimit") {
		t.Fatalf("exit=%d err=%q, want a usage error naming --bwlimit", code, errOut)
	}
}

// dropSoon severs the server's live connections once the transfer is under way.
func dropSoon(e *e2eEnv, after time.Duration) {
	go func() {
		time.Sleep(after)
		e.drop()
	}()
}

// fastRetryApp is e.do with the reconnect backoff removed, so a dropped
// connection costs milliseconds, and the reconnect notices captured.
func (e *e2eEnv) doFast(t *testing.T, cmd string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	app := App{
		Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut,
		Dial:       connect.Dialer,
		ConfigPath: filepath.Join(t.TempDir(), "none.toml"),
		Sleep:      func(time.Duration) {},
	}
	full := append([]string{cmd}, e.flags...)
	full = append(full, args...)
	code := app.Run(full)
	return code, out.String(), errOut.String()
}

func TestE2EResumesDownloadAfterConnectionDrop(t *testing.T) {
	if testing.Short() {
		t.Skip("paced transfers take several seconds")
	}
	forEachProtocol(t, func(t *testing.T, e *e2eEnv) {
		if e.name == "ftp" {
			t.Skip("the test server can only drop FTP control connections, which does not interrupt a download already flowing over its separate data connection")
		}
		body := make([]byte, 768<<10)
		_, _ = rand.Read(body)
		e.write(t, "drop-down.bin", body)
		dest := filepath.Join(t.TempDir(), "out.bin")

		dropSoon(e, 1*time.Second) // ~256 KiB in at 256 KiB/s
		code, _, errOut := e.doFast(t, "get", "--bwlimit", "256k", "--retries", "5", e.rp("drop-down.bin"), dest)
		if code != 0 {
			t.Fatalf("exit %d, stderr=%s", code, errOut)
		}
		if !strings.Contains(errOut, "reconnecting") {
			t.Fatalf("the drop never happened (no reconnect notice): %s", errOut)
		}
		got, _ := os.ReadFile(dest)
		if !bytes.Equal(got, body) {
			t.Fatalf("resumed download is corrupt (len %d, want %d)", len(got), len(body))
		}
	})
}

func TestE2EResumesUploadAfterConnectionDrop(t *testing.T) {
	if testing.Short() {
		t.Skip("paced transfers take several seconds")
	}
	forEachProtocol(t, func(t *testing.T, e *e2eEnv) {
		body := make([]byte, 768<<10)
		_, _ = rand.Read(body)
		src := writeLocal(t, t.TempDir(), "drop-up.bin", string(body))

		dropSoon(e, 1*time.Second)
		code, _, errOut := e.doFast(t, "put", "--bwlimit", "256k", "--retries", "5", src, e.rp("drop-up.bin"))
		if code != 0 {
			t.Fatalf("exit %d, stderr=%s", code, errOut)
		}
		if !strings.Contains(errOut, "reconnecting") {
			t.Fatalf("the drop never happened (no reconnect notice): %s", errOut)
		}
		if got := e.read(t, "drop-up.bin"); got != string(body) {
			t.Fatalf("resumed upload is corrupt (len %d, want %d)", len(got), len(body))
		}
		if e.read(t, "drop-up.bin.part") != "<missing>" {
			t.Fatalf(".part left behind")
		}
	})
}

func TestE2EWithoutRetriesADropFails(t *testing.T) {
	if testing.Short() {
		t.Skip("paced transfers take several seconds")
	}
	forEachProtocol(t, func(t *testing.T, e *e2eEnv) {
		if e.name == "ftp" {
			t.Skip("the test server can only drop FTP control connections, which does not interrupt a download already flowing over its separate data connection")
		}
		body := make([]byte, 768<<10)
		e.write(t, "nodrop.bin", body)
		dropSoon(e, 800*time.Millisecond)
		code, _, _ := e.doFast(t, "get", "--bwlimit", "256k", e.rp("nodrop.bin"), filepath.Join(t.TempDir(), "o.bin"))
		if code == 0 {
			t.Fatalf("a dropped connection with --retries 0 should fail the command")
		}
	})
}

func TestE2ESyncSurvivesConnectionDrop(t *testing.T) {
	if testing.Short() {
		t.Skip("paced transfers take several seconds")
	}
	forEachProtocol(t, func(t *testing.T, e *e2eEnv) {
		src := t.TempDir()
		for i := 0; i < 4; i++ {
			b := make([]byte, 128<<10)
			_, _ = rand.Read(b)
			writeLocal(t, src, filepath.Join("d", string(rune('a'+i))+".bin"), string(b))
		}
		dropSoon(e, 700*time.Millisecond)
		code, _, errOut := e.doFast(t, "sync", "--bwlimit", "256k", "--transfers", "2", src, ":"+e.rp("dropsync"))
		if code != 0 {
			t.Fatalf("exit %d, stderr=%s", code, errOut)
		}
		for i := 0; i < 4; i++ {
			name := filepath.Join("d", string(rune('a'+i))+".bin")
			want, _ := os.ReadFile(filepath.Join(src, name))
			if got := e.read(t, "dropsync/"+filepath.ToSlash(name)); got != string(want) {
				t.Fatalf("%s differs after a mid-sync drop", name)
			}
		}
	})
}

func TestE2EPutResumeContinuesPartFile(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e *e2eEnv) {
		body := make([]byte, 200<<10)
		_, _ = rand.Read(body)
		src := writeLocal(t, t.TempDir(), "r.bin", string(body))
		half := len(body) / 2
		// A leftover .part whose first half is zeros, not the real data: if
		// the upload truly continues from it, the zeros survive in the result;
		// if it restarted, they would be overwritten.
		e.write(t, "r.bin.part", make([]byte, half))

		e.mustDo(t, "put", "--resume", src, e.rp("r.bin"))
		want := append(make([]byte, half), body[half:]...)
		if got := e.read(t, "r.bin"); got != string(want) {
			t.Fatalf("upload did not continue from the existing .part (len %d)", len(got))
		}
		if e.read(t, "r.bin.part") != "<missing>" {
			t.Fatalf(".part left behind after the rename")
		}
		// Completed already: reput is a no-op and says so.
		code, _, errOut := e.do(t, "reput", src, e.rp("r.bin"))
		if code != 0 || !strings.Contains(errOut, "already complete") {
			t.Fatalf("reput on a finished file: exit %d, stderr=%s", code, errOut)
		}
	})
}

func TestE2EWithoutResumeAStalePartIsReplaced(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e *e2eEnv) {
		body := make([]byte, 100<<10)
		_, _ = rand.Read(body)
		src := writeLocal(t, t.TempDir(), "s.bin", string(body))
		e.write(t, "s.bin.part", make([]byte, 50<<10))
		e.mustDo(t, "put", src, e.rp("s.bin"))
		if e.read(t, "s.bin") != string(body) {
			t.Fatalf("a stale .part leaked into an upload that was not asked to resume")
		}
	})
}

func TestE2ESyncResumeContinuesUpload(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e *e2eEnv) {
		body := make([]byte, 100<<10)
		_, _ = rand.Read(body)
		src := t.TempDir()
		writeLocal(t, src, "x.bin", string(body))
		e.write(t, "rs/x.bin.part", make([]byte, 40<<10))
		e.mustDo(t, "sync", "--resume", src, ":"+e.rp("rs"))
		want := append(make([]byte, 40<<10), body[40<<10:]...)
		if e.read(t, "rs/x.bin") != string(want) {
			t.Fatalf("sync --resume restarted instead of continuing")
		}
	})
}

func TestE2EUploadPreservesMtimeAndMode(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e *e2eEnv) {
		src := t.TempDir()
		p := writeLocal(t, src, "keep.txt", "keep")
		stamp := time.Now().Add(-96 * time.Hour).Truncate(time.Second)
		_ = os.Chtimes(p, stamp, stamp)
		_ = os.Chmod(p, 0o640)

		e.mustDo(t, "sync", src, ":"+e.rp("meta"))
		info, err := os.Stat(e.disk("meta/keep.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if e.name == "sftp" {
			if !info.ModTime().Equal(stamp) {
				t.Fatalf("remote mtime = %v, want %v", info.ModTime(), stamp)
			}
			if info.Mode().Perm() != 0o640 {
				t.Fatalf("remote mode = %v, want 0640", info.Mode().Perm())
			}
		} else if !info.ModTime().Equal(stamp) {
			t.Logf("this FTP server did not apply MFMT (mtime %v, wanted %v); sync degrades to size/upload-time comparison", info.ModTime(), stamp)
		}
		// Whatever the server could do, the next run must be a no-op.
		if out := e.mustDo(t, "sync", src, ":"+e.rp("meta")); strings.Contains(out, "update") || strings.Contains(out, "copy") {
			t.Fatalf("second sync not idempotent: %q", out)
		}
	})
}

func TestE2EKillStopsABackgroundTransfer(t *testing.T) {
	if testing.Short() {
		t.Skip("paced transfers take several seconds")
	}
	forEachProtocol(t, func(t *testing.T, e *e2eEnv) {
		body := make([]byte, 2<<20)
		src := writeLocal(t, t.TempDir(), "huge.bin", string(body))
		start := time.Now()
		// 2 MiB at 64 KiB/s would take half a minute; killing it must not.
		code, out, errOut := e.doFast(t, "script", "-c",
			"set net:limit-rate 64k\nput "+src+" "+e.rp("huge.bin")+" &\nset net:limit-rate 0\njobs\n!sleep 1\nkill 1\nwait\n")
		if code != 0 {
			t.Fatalf("exit %d\nstdout=%s\nstderr=%s", code, out, errOut)
		}
		if time.Since(start) > 10*time.Second {
			t.Fatalf("kill did not stop the paced upload (took %v)", time.Since(start))
		}
		if !strings.Contains(out, "Running") {
			t.Fatalf("`jobs` showed nothing while the upload ran: %q", out)
		}
		if !strings.Contains(errOut, "Killed:") {
			t.Fatalf("no kill notice: %q", errOut)
		}
		if e.read(t, "huge.bin") != "<missing>" {
			t.Fatalf("a killed upload must not leave a finished file")
		}
	})
}

func TestE2EPgetSegmentedDownload(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e *e2eEnv) {
		body := make([]byte, 5<<20+4097) // an odd size, so the last segment is the ragged one
		_, _ = rand.Read(body)
		e.write(t, "seg.bin", body)
		dest := filepath.Join(t.TempDir(), "seg.bin")

		out, errOut := "", ""
		code, o, eo := e.do(t, "pget", "-n", "4", e.rp("seg.bin"), dest)
		out, errOut = o, eo
		if code != 0 {
			t.Fatalf("exit %d\nstdout=%s\nstderr=%s", code, out, errOut)
		}
		if !strings.Contains(errOut, "4 connections") {
			t.Fatalf("pget did not announce its segments: %q", errOut)
		}
		got, _ := os.ReadFile(dest)
		if !bytes.Equal(got, body) {
			t.Fatalf("segmented download differs (len %d vs %d)", len(got), len(body))
		}
		if _, err := os.Stat(dest + ".part"); err == nil {
			t.Fatalf(".part left behind")
		}
		// A refusal to overwrite, then --force.
		if code, _, _ := e.do(t, "pget", e.rp("seg.bin"), dest); code != 1 {
			t.Fatalf("pget over an existing file = %d, want 1", code)
		}
		e.mustDo(t, "pget", "-n", "3", "--force", e.rp("seg.bin"), dest)
		// Small files and -n 1 take the plain path.
		e.write(t, "tiny.bin", []byte("tiny"))
		tiny := filepath.Join(t.TempDir(), "tiny.bin")
		e.mustDo(t, "pget", "-n", "8", e.rp("tiny.bin"), tiny)
		if b, _ := os.ReadFile(tiny); string(b) != "tiny" {
			t.Fatalf("small file mishandled")
		}
	})
}

func TestE2ESyncUsePgetN(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e *e2eEnv) {
		body := make([]byte, 3<<20+11)
		_, _ = rand.Read(body)
		e.write(t, "p/big.bin", body)
		e.write(t, "p/small.txt", []byte("small"))
		dest := t.TempDir()
		e.mustDo(t, "sync", "--use-pget-n", "3", "--pget-min-size", "1M", ":"+e.rp("p"), dest)
		if b, _ := os.ReadFile(filepath.Join(dest, "big.bin")); !bytes.Equal(b, body) {
			t.Fatalf("sync with pget corrupted the big file")
		}
		if b, _ := os.ReadFile(filepath.Join(dest, "small.txt")); string(b) != "small" {
			t.Fatalf("small file missing")
		}
		if out := e.mustDo(t, "sync", "--use-pget-n", "3", ":"+e.rp("p"), dest); strings.Contains(out, "update") {
			t.Fatalf("second run not idempotent: %q", out)
		}
	})
}

func TestE2EGetOutputDirAndRmdir(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e *e2eEnv) {
		e.write(t, "o/one.txt", []byte("1"))
		e.write(t, "o/two.txt", []byte("2"))
		out := filepath.Join(t.TempDir(), "made", "here")
		e.mustDo(t, "mget", "-O", out, e.rp("o/*.txt"))
		if b, _ := os.ReadFile(filepath.Join(out, "two.txt")); string(b) != "2" {
			t.Fatalf("-O did not create and fill the directory")
		}
		src := writeLocal(t, t.TempDir(), "u.txt", "U")
		e.mustDo(t, "mkdir", e.rp("pdir"))
		e.mustDo(t, "put", "-O", e.rp("pdir"), src)
		if e.read(t, "pdir/u.txt") != "U" {
			t.Fatalf("put -O did not upload into the directory")
		}
		// goftp's test server deletes directories recursively on RMD (real
		// servers refuse a non-empty one), so only SFTP can assert the refusal.
		if e.name == "sftp" {
			if code, _, _ := e.do(t, "rmdir", e.rp("pdir")); code == 0 {
				t.Fatalf("rmdir removed a non-empty directory")
			}
		}
		e.mustDo(t, "mrm", e.rp("pdir/u.txt"))
		e.mustDo(t, "rmdir", e.rp("pdir"))
		if code, _, _ := e.do(t, "exists", e.rp("pdir")); code != 5 {
			t.Fatalf("rmdir left the directory")
		}
	})
}

func TestE2ESymlinks(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e *e2eEnv) {
		e.write(t, "ln/target.txt", []byte("t"))
		abs := e.rp("ln/target.txt") // absolute: servers differ on how they store a relative target
		code, _, errOut := e.do(t, "ln", "-s", abs, e.rp("ln/link"))
		if e.name == "ftp" {
			if code != 1 || !strings.Contains(errOut, "not supported") {
				t.Fatalf("FTP has no symlinks: exit %d err=%q, want a clear 'not supported'", code, errOut)
			}
			if code, _, _ := e.do(t, "readlink", e.rp("ln/target.txt")); code != 1 {
				t.Fatalf("readlink over FTP = %d, want 1", code)
			}
			return
		}
		if code != 0 {
			t.Fatalf("ln -s: exit %d, stderr=%s", code, errOut)
		}
		if dest, err := os.Readlink(e.disk("ln/link")); err != nil || dest != abs {
			t.Fatalf("on-disk link = %q, %v; want %q", dest, err, abs)
		}
		if out := e.mustDo(t, "readlink", e.rp("ln/link")); strings.TrimSpace(out) != abs {
			t.Fatalf("readlink = %q", out)
		}
		if !strings.Contains(e.mustDo(t, "ls", "--json", e.rp("ln")), `"type": "symlink"`) {
			t.Fatalf("ls --json does not report the link as a symlink")
		}
		// Existing link: refused without -f, replaced with it.
		if code, _, _ := e.do(t, "ln", "-s", e.rp("ln/other"), e.rp("ln/link")); code == 0 {
			t.Fatalf("ln -s over an existing link should fail without -f")
		}
		other := e.rp("ln/other")
		e.mustDo(t, "ln", "-s", "-f", other, e.rp("ln/link"))
		if dest, _ := os.Readlink(e.disk("ln/link")); dest != other {
			t.Fatalf("-f did not replace the link (now %q)", dest)
		}
		if code, _, _ := e.do(t, "ln", e.rp("ln/target.txt"), e.rp("ln/hard")); code != 2 {
			t.Fatalf("ln without -s = %d, want a usage error", code)
		}
	})
}

// The deploy recipe in docs/scripting.md, run for real over SFTP (ln -s needs it).
func TestE2EGuideDeployRecipe(t *testing.T) {
	envs := e2eEnvs(t)
	e := envs[0]
	if e.name != "sftp" {
		t.Fatal("expected sftp first")
	}
	build := t.TempDir()
	writeLocal(t, build, "index.html", "<h1>v1</h1>")
	writeLocal(t, build, "assets/app.js", "js")
	t.Chdir(build)

	rel := e.rp("www/releases/2026-10-08-0900")
	script := "\nlcd " + build + "\nmkdir -p " + rel + "\nmirror -R --delete . " + rel + "\nln -s -f " + rel + " " + e.rp("www/current") + "\n"
	e.mustDo(t, "script", "-c", script)
	if e.read(t, "www/releases/2026-10-08-0900/assets/app.js") != "js" {
		t.Fatalf("release not uploaded")
	}
	if dest, err := os.Readlink(e.disk("www/current")); err != nil || dest != rel {
		t.Fatalf("current -> %q, %v; want %q", dest, err, rel)
	}
	// Run again with a new release: current is repointed (-f), the old release stays.
	writeLocal(t, build, "index.html", "<h1>v2</h1>")
	rel2 := e.rp("www/releases/2026-10-08-0930")
	e.mustDo(t, "script", "-c", "lcd "+build+"\nmkdir -p "+rel2+"\nmirror -R --delete . "+rel2+"\nln -s -f "+rel2+" "+e.rp("www/current")+"\n")
	if dest, _ := os.Readlink(e.disk("www/current")); dest != rel2 {
		t.Fatalf("current not repointed: %q", dest)
	}
	if e.read(t, "www/releases/2026-10-08-0900/index.html") != "<h1>v1</h1>" {
		t.Fatalf("the previous release was disturbed")
	}
}
