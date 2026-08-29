package sftpsession

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"tideftp/internal/domain"
	"tideftp/internal/session"
	"tideftp/internal/transfer"
)

func targetFor(server *testServer) session.Target {
	host, port, _ := strings.Cut(server.addr, ":")
	target := session.Target{Protocol: "sftp", Host: host, User: "tester", StartPath: server.root}
	target.Port = atoi(port)
	return target
}

func atoi(value string) int {
	n := 0
	for _, r := range value {
		n = n*10 + int(r-'0')
	}
	return n
}

func dialerFor(t *testing.T, server *testServer) *Dialer {
	t.Helper()
	return New(Config{
		KnownHostsPath: server.knownHostsFile(t),
		IdentityFiles:  []string{server.clientPK},
		Timeout:        10 * time.Second,
	})
}

func connect(t *testing.T, server *testServer) session.Conn {
	t.Helper()
	conn, err := dialerFor(t, server).Dial(context.Background(), targetFor(server), session.Credentials{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestDialWithAKeyFile(t *testing.T) {
	server := startTestServer(t)
	conn := connect(t, server)

	if conn.FS() == nil || conn.Engine() == nil {
		t.Fatalf("a live connection must expose both an FS and an engine")
	}
}

func TestDialWithAPassphraseProtectedKey(t *testing.T) {
	server := startTestServer(t)

	block, err := ssh.MarshalPrivateKeyWithPassphrase(server.clientKey, "encrypted test key", []byte("hunter2"))
	if err != nil {
		t.Fatalf("marshal encrypted key: %v", err)
	}
	encPath := filepath.Join(t.TempDir(), "id_ed25519_enc")
	if err := os.WriteFile(encPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write encrypted key: %v", err)
	}

	dialer := New(Config{KnownHostsPath: server.knownHostsFile(t), Timeout: 10 * time.Second})
	target := targetFor(server)

	// Without the passphrase the encrypted key is unusable and Dial says so.
	_, err = dialer.Dial(context.Background(), target, session.Credentials{IdentityFile: encPath})
	if err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("Dial without passphrase = %v, want an error mentioning the passphrase", err)
	}

	// With it, the key decrypts and the connection comes up.
	conn, err := dialer.Dial(context.Background(), target, session.Credentials{IdentityFile: encPath, KeyPassphrase: "hunter2"})
	if err != nil {
		t.Fatalf("Dial with passphrase: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if conn.FS() == nil {
		t.Fatalf("connection with a decrypted key exposed no FS")
	}

	// A wrong passphrase fails cleanly rather than hanging or panicking.
	_, err = dialer.Dial(context.Background(), target, session.Credentials{IdentityFile: encPath, KeyPassphrase: "wrong"})
	if err == nil {
		t.Fatalf("Dial with the wrong passphrase succeeded")
	}
}

func TestDialStrictPolicyRejectsAnUnknownHostWithoutPrompting(t *testing.T) {
	server := startTestServer(t)
	dialer := New(Config{
		KnownHostsPath: server.emptyKnownHostsFile(t),
		IdentityFiles:  []string{server.clientPK},
		Timeout:        10 * time.Second,
	})
	target := targetFor(server)
	target.HostKeyPolicy = session.HostKeyStrict

	_, err := dialer.Dial(context.Background(), target, session.Credentials{})
	if err == nil {
		t.Fatalf("strict policy accepted a host with no known_hosts entry")
	}
	var ask *session.UntrustedHostKeyError
	if errors.As(err, &ask) {
		t.Fatalf("strict policy raised the accept-once prompt instead of failing: %v", err)
	}
}

func TestDialOffPolicyAcceptsAnUnknownHost(t *testing.T) {
	server := startTestServer(t)
	dialer := New(Config{
		KnownHostsPath: server.emptyKnownHostsFile(t),
		IdentityFiles:  []string{server.clientPK},
		Timeout:        10 * time.Second,
	})
	target := targetFor(server)
	target.HostKeyPolicy = session.HostKeyOff

	conn, err := dialer.Dial(context.Background(), target, session.Credentials{})
	if err != nil {
		t.Fatalf("off policy rejected an unknown host: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if conn.FS() == nil {
		t.Fatalf("connection with host-key checking off exposed no FS")
	}
}

func TestDialRejectsAMismatchedHostKey(t *testing.T) {
	server := startTestServer(t)
	dialer := New(Config{
		KnownHostsPath: server.wrongKnownHostsFile(t),
		IdentityFiles:  []string{server.clientPK},
		Timeout:        10 * time.Second,
	})

	_, err := dialer.Dial(context.Background(), targetFor(server), session.Credentials{})
	if err == nil {
		t.Fatalf("dialing a server whose host key does not match known_hosts succeeded")
	}
	if !strings.Contains(err.Error(), "knownhosts") && !strings.Contains(err.Error(), "host key") {
		t.Fatalf("error = %v, want it to name the host key mismatch", err)
	}
	var hostKeyErr *session.UntrustedHostKeyError
	if errors.As(err, &hostKeyErr) {
		t.Fatalf("a real mismatch must never surface as UntrustedHostKeyError — that type means 'ask the user', not 'this is wrong'")
	}
}

func TestDialTrustedHostKeyNeverOverridesAKnownMismatch(t *testing.T) {
	server := startTestServer(t)
	dialer := New(Config{
		KnownHostsPath: server.wrongKnownHostsFile(t),
		IdentityFiles:  []string{server.clientPK},
		Timeout:        10 * time.Second,
	})

	// TrustedHostKey names the server's real key — the one an honest dial
	// would actually receive — not the wrong one known_hosts already pins.
	// If accept-once could override a mismatch, this would be a downgrade
	// attack; it must still fail exactly as TestDialRejectsAMismatchedHostKey
	// does.
	_, err := dialer.Dial(context.Background(), targetFor(server), session.Credentials{
		TrustedHostKey: server.hostKey.Marshal(),
	})
	if err == nil {
		t.Fatalf("a TrustedHostKey override must never accept a host whose known_hosts entry says it changed")
	}
}

func TestDialReturnsUntrustedHostKeyErrorForAnUnknownHost(t *testing.T) {
	server := startTestServer(t)
	dialer := New(Config{
		KnownHostsPath: server.emptyKnownHostsFile(t),
		IdentityFiles:  []string{server.clientPK},
		Timeout:        10 * time.Second,
	})

	_, err := dialer.Dial(context.Background(), targetFor(server), session.Credentials{})
	var hostKeyErr *session.UntrustedHostKeyError
	if !errors.As(err, &hostKeyErr) {
		t.Fatalf("Dial against a host with no known_hosts entry = %v, want an UntrustedHostKeyError", err)
	}
	if hostKeyErr.Algorithm == "" || hostKeyErr.Fingerprint == "" || hostKeyErr.Address == "" {
		t.Fatalf("UntrustedHostKeyError = %+v, want Algorithm/Fingerprint/Address all populated", hostKeyErr)
	}
	if !bytes.Equal(hostKeyErr.Key, server.hostKey.Marshal()) {
		t.Fatalf("UntrustedHostKeyError.Key does not match the server's actual host key")
	}
}

func TestDialAcceptsATrustedHostKeyForOneAttemptOnly(t *testing.T) {
	server := startTestServer(t)
	path := server.emptyKnownHostsFile(t)
	dialer := New(Config{
		KnownHostsPath: path,
		IdentityFiles:  []string{server.clientPK},
		Timeout:        10 * time.Second,
	})

	_, err := dialer.Dial(context.Background(), targetFor(server), session.Credentials{})
	var hostKeyErr *session.UntrustedHostKeyError
	if !errors.As(err, &hostKeyErr) {
		t.Fatalf("first dial = %v, want an UntrustedHostKeyError to set up the accept-once retry", err)
	}

	conn, err := dialer.Dial(context.Background(), targetFor(server), session.Credentials{
		TrustedHostKey: hostKeyErr.Key,
	})
	if err != nil {
		t.Fatalf("dial with a matching TrustedHostKey: %v", err)
	}
	conn.Close()

	// Nothing was remembered, so a plain retry against the same file must be
	// asked again rather than silently trusting the host from now on.
	_, err = dialer.Dial(context.Background(), targetFor(server), session.Credentials{})
	if !errors.As(err, &hostKeyErr) {
		t.Fatalf("a follow-up dial without TrustedHostKey = %v, want it to be asked again (accept-once must not persist)", err)
	}
}

func TestDialRemembersATrustedHostKeyWhenRequested(t *testing.T) {
	server := startTestServer(t)
	path := server.emptyKnownHostsFile(t)
	dialer := New(Config{
		KnownHostsPath: path,
		IdentityFiles:  []string{server.clientPK},
		Timeout:        10 * time.Second,
	})

	_, err := dialer.Dial(context.Background(), targetFor(server), session.Credentials{})
	var hostKeyErr *session.UntrustedHostKeyError
	if !errors.As(err, &hostKeyErr) {
		t.Fatalf("first dial = %v, want an UntrustedHostKeyError", err)
	}

	conn, err := dialer.Dial(context.Background(), targetFor(server), session.Credentials{
		TrustedHostKey:  hostKeyErr.Key,
		RememberHostKey: true,
	})
	if err != nil {
		t.Fatalf("dial with TrustedHostKey and RememberHostKey: %v", err)
	}
	conn.Close()

	// Remembered, so a plain retry against the same file must now succeed
	// without being asked again.
	conn, err = dialer.Dial(context.Background(), targetFor(server), session.Credentials{})
	if err != nil {
		t.Fatalf("dial after remembering the host key: %v", err)
	}
	conn.Close()
}

func TestDialIgnoresATrustedHostKeyThatDoesNotMatchTheServer(t *testing.T) {
	server := startTestServer(t)
	_, otherPub := newSigner(t)
	dialer := New(Config{
		KnownHostsPath: server.emptyKnownHostsFile(t),
		IdentityFiles:  []string{server.clientPK},
		Timeout:        10 * time.Second,
	})

	_, err := dialer.Dial(context.Background(), targetFor(server), session.Credentials{
		TrustedHostKey: otherPub.Marshal(),
	})
	var hostKeyErr *session.UntrustedHostKeyError
	if !errors.As(err, &hostKeyErr) {
		t.Fatalf("a TrustedHostKey that does not match what the server actually presents must still be asked about, got %v", err)
	}
}

func TestDialFailsWithoutCredentials(t *testing.T) {
	server := startTestServer(t)
	t.Setenv("SSH_AUTH_SOCK", "")
	dialer := New(Config{KnownHostsPath: server.knownHostsFile(t)})

	_, err := dialer.Dial(context.Background(), targetFor(server), session.Credentials{})
	if err == nil {
		t.Fatalf("dialing with no keys and no agent succeeded")
	}
	if !strings.Contains(err.Error(), "no usable credentials") {
		t.Fatalf("error = %v, want it to explain there are no credentials", err)
	}
}

func TestDialWithIdentityFileOverrideReplacesConfiguredKeys(t *testing.T) {
	server := startTestServer(t)
	// dialerFor configures a valid key file that TestDialWithAKeyFile proves
	// succeeds on its own; an IdentityFile override must replace it, not add
	// to it, so pointing the override at a key that doesn't exist fails the
	// dial even though the Dialer's own configured key would have worked.
	dialer := dialerFor(t, server)

	_, err := dialer.Dial(context.Background(), targetFor(server), session.Credentials{
		IdentityFile: filepath.Join(t.TempDir(), "no-such-key"),
	})
	if err == nil {
		t.Fatalf("an IdentityFile override should have replaced the working configured key, but the dial succeeded")
	}
}

func TestDialWithKnownHostsOverrideVerifiesAgainstIt(t *testing.T) {
	server := startTestServer(t)
	// Configured with a known_hosts that does NOT have this server's key —
	// TestDialRejectsAnUnknownHostKey proves that fails on its own — so a
	// successful dial here can only be explained by the override actually
	// being used instead.
	dialer := New(Config{
		KnownHostsPath: server.wrongKnownHostsFile(t),
		IdentityFiles:  []string{server.clientPK},
		Timeout:        10 * time.Second,
	})

	conn, err := dialer.Dial(context.Background(), targetFor(server), session.Credentials{
		KnownHostsPath: server.knownHostsFile(t),
	})
	if err != nil {
		t.Fatalf("Dial with a KnownHostsPath override: %v", err)
	}
	conn.Close()
}

func TestDialWithPasswordOnlySkipsConfiguredKeyFiles(t *testing.T) {
	server := startTestServer(t)
	// dialerFor configures a valid key file that TestDialWithAKeyFile proves
	// succeeds on its own; PasswordOnly must still refuse to use it.
	dialer := dialerFor(t, server)

	_, err := dialer.Dial(context.Background(), targetFor(server), session.Credentials{PasswordOnly: true, Password: "whatever"})
	if err == nil {
		t.Fatalf("PasswordOnly should have skipped the configured key file, but the dial succeeded")
	}
}

func TestDialRejectsPasswordOnlyWithNoPassword(t *testing.T) {
	server := startTestServer(t)
	dialer := dialerFor(t, server)

	_, err := dialer.Dial(context.Background(), targetFor(server), session.Credentials{PasswordOnly: true})
	if err == nil || !strings.Contains(err.Error(), "no password was given") {
		t.Fatalf("error = %v, want it to explain that password auth needs a password", err)
	}
}

func TestDialWithNoKnownHostsFileTreatsTheHostAsUnknown(t *testing.T) {
	server := startTestServer(t)
	path := filepath.Join(t.TempDir(), "nested", "known_hosts")
	dialer := New(Config{
		KnownHostsPath: path,
		IdentityFiles:  []string{server.clientPK},
	})

	// A missing known_hosts file must never fall back to silently accepting
	// whatever key the server offers — but it also must not be a dead end on
	// a fresh machine: it's created empty, and the host reports as unknown
	// through the same ask/accept-once flow an existing-but-empty file would.
	_, err := dialer.Dial(context.Background(), targetFor(server), session.Credentials{})
	var hostKeyErr *session.UntrustedHostKeyError
	if !errors.As(err, &hostKeyErr) {
		t.Fatalf("dial with no known_hosts file = %v, want an UntrustedHostKeyError rather than a silent accept or a bare file error", err)
	}
	if info, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("expected an empty known_hosts file to be created at %s: %v", path, statErr)
	} else if info.Size() != 0 {
		t.Fatalf("expected the auto-created known_hosts file to start empty, got %d bytes", info.Size())
	}
}

func TestDialFailsForARefusedServer(t *testing.T) {
	server := startTestServer(t)
	server.refuse()

	if _, err := dialerFor(t, server).Dial(context.Background(), targetFor(server), session.Credentials{}); err == nil {
		t.Fatalf("dialing a server that drops connections succeeded")
	}
}

func TestListReadsTheRemoteDirectory(t *testing.T) {
	server := startTestServer(t)
	server.writeFile(t, "beta.txt", []byte("beta"))
	server.writeFile(t, "Alpha.txt", []byte("alpha"))
	server.writeFile(t, ".hidden", []byte("secret"))
	server.writeFile(t, "sub/nested.txt", []byte("nested"))

	fs := connect(t, server).FS()

	entries, err := fs.List(context.Background(), server.root, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name)
	}
	want := []string{"sub", "Alpha.txt", "beta.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("entries = %v, want %v (dirs first, hidden omitted)", got, want)
	}
	if entries[0].Kind != domain.EntryDir {
		t.Fatalf("sub is not reported as a directory")
	}

	withHidden, err := fs.List(context.Background(), server.root, true)
	if err != nil {
		t.Fatalf("List(showHidden): %v", err)
	}
	if len(withHidden) != 4 {
		t.Fatalf("showHidden returned %d entries, want 4", len(withHidden))
	}
}

func TestListReportsAMissingDirectory(t *testing.T) {
	server := startTestServer(t)
	fs := connect(t, server).FS()

	if _, err := fs.List(context.Background(), server.path("nope"), false); err == nil {
		t.Fatalf("listing a missing directory returned no error")
	}
}

func TestListHonoursContextCancellation(t *testing.T) {
	server := startTestServer(t)
	fs := connect(t, server).FS()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fs.List(ctx, server.root, false); err == nil {
		t.Fatalf("listing with a cancelled context returned no error")
	}
}

func TestChildAndParent(t *testing.T) {
	fs := &FS{}
	if got := fs.Child("/public_html", "index.html"); got != "/public_html/index.html" {
		t.Fatalf("Child = %q", got)
	}
	if got := fs.Parent("/public_html/assets"); got != "/public_html" {
		t.Fatalf("Parent = %q", got)
	}
	if got := fs.Parent("/"); got != "/" {
		t.Fatalf("Parent(\"/\") = %q, want / so the UI stops walking up", got)
	}
}

// awaitTerminal drains events for one transfer until its terminal event.
func awaitTerminal(t *testing.T, engine transfer.Engine, id int) transfer.Event {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case event, ok := <-engine.Events():
			if !ok {
				t.Fatalf("engine closed its stream before transfer %d finished", id)
			}
			if event.ID == id && event.Terminal() {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for transfer %d", id)
		}
	}
}

func TestUploadWritesTheFileToTheServer(t *testing.T) {
	server := startTestServer(t)
	conn := connect(t, server)

	body := bytes.Repeat([]byte("tideftp upload\n"), 5000)
	local := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(local, body, 0o644); err != nil {
		t.Fatal(err)
	}

	conn.Engine().Start(transfer.Request{
		ID: 1, Direction: domain.Upload,
		Source: local, Destination: server.path("uploaded.bin"), Size: int64(len(body)),
	})
	event := awaitTerminal(t, conn.Engine(), 1)

	if event.Kind != transfer.Completed {
		t.Fatalf("terminal event = %v (err %v), want Completed", event.Kind, event.Err)
	}
	if event.BytesDone != int64(len(body)) {
		t.Fatalf("reported %d bytes, want %d", event.BytesDone, len(body))
	}
	got, err := os.ReadFile(server.path("uploaded.bin"))
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("uploaded content differs: %d bytes on the server, %d sent", len(got), len(body))
	}
}

func TestDownloadWritesTheFileLocally(t *testing.T) {
	server := startTestServer(t)
	body := bytes.Repeat([]byte("tideftp download\n"), 5000)
	server.writeFile(t, "remote.bin", body)
	conn := connect(t, server)

	local := filepath.Join(t.TempDir(), "nested", "remote.bin")
	conn.Engine().Start(transfer.Request{
		ID: 7, Direction: domain.Download,
		Source: server.path("remote.bin"), Destination: local, Size: int64(len(body)),
	})
	event := awaitTerminal(t, conn.Engine(), 7)

	if event.Kind != transfer.Completed {
		t.Fatalf("terminal event = %v (err %v), want Completed", event.Kind, event.Err)
	}
	got, err := os.ReadFile(local)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("downloaded content differs: %d bytes locally, %d on the server", len(got), len(body))
	}
}

func TestUploadResumesFromOffset(t *testing.T) {
	server := startTestServer(t)
	conn := connect(t, server)

	body := bytes.Repeat([]byte("tideftp resume upload\n"), 5000)
	local := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(local, body, 0o644); err != nil {
		t.Fatal(err)
	}

	// A partial destination that genuinely matches the source's own first
	// half — Offset only tells the engine where to start; it never verifies
	// the bytes already there, that's the conflict policy's job upstream.
	offset := int64(len(body) / 2)
	if err := os.WriteFile(server.path("resumed.bin"), body[:offset], 0o644); err != nil {
		t.Fatal(err)
	}

	conn.Engine().Start(transfer.Request{
		ID: 20, Direction: domain.Upload,
		Source: local, Destination: server.path("resumed.bin"), Size: int64(len(body)), Offset: offset,
	})
	event := awaitTerminal(t, conn.Engine(), 20)

	if event.Kind != transfer.Completed {
		t.Fatalf("terminal event = %v (err %v), want Completed", event.Kind, event.Err)
	}
	if event.BytesDone != int64(len(body)) {
		t.Fatalf("reported %d bytes done, want the full %d (offset plus what was actually copied)", event.BytesDone, len(body))
	}
	got, err := os.ReadFile(server.path("resumed.bin"))
	if err != nil {
		t.Fatalf("read resumed file: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("resumed content differs from the full source: got %d bytes, want %d", len(got), len(body))
	}
}

func TestDownloadResumesFromOffset(t *testing.T) {
	server := startTestServer(t)
	body := bytes.Repeat([]byte("tideftp resume download\n"), 5000)
	server.writeFile(t, "remote.bin", body)
	conn := connect(t, server)

	offset := int64(len(body) / 3)
	local := filepath.Join(t.TempDir(), "resumed.bin")
	if err := os.WriteFile(local, body[:offset], 0o644); err != nil {
		t.Fatal(err)
	}

	conn.Engine().Start(transfer.Request{
		ID: 21, Direction: domain.Download,
		Source: server.path("remote.bin"), Destination: local, Size: int64(len(body)), Offset: offset,
	})
	event := awaitTerminal(t, conn.Engine(), 21)

	if event.Kind != transfer.Completed {
		t.Fatalf("terminal event = %v (err %v), want Completed", event.Kind, event.Err)
	}
	if event.BytesDone != int64(len(body)) {
		t.Fatalf("reported %d bytes done, want the full %d", event.BytesDone, len(body))
	}
	got, err := os.ReadFile(local)
	if err != nil {
		t.Fatalf("read resumed file: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("resumed content differs from the full source: got %d bytes, want %d", len(got), len(body))
	}
}

func TestTransferOfAMissingSourceFails(t *testing.T) {
	server := startTestServer(t)
	conn := connect(t, server)

	conn.Engine().Start(transfer.Request{
		ID: 3, Direction: domain.Download,
		Source: server.path("absent.bin"), Destination: filepath.Join(t.TempDir(), "out.bin"), Size: 10,
	})
	event := awaitTerminal(t, conn.Engine(), 3)

	if event.Kind != transfer.Failed {
		t.Fatalf("terminal event = %v, want Failed", event.Kind)
	}
	if event.Err == nil {
		t.Fatalf("a failed transfer must report why")
	}
}

func TestCancelStopsATransfer(t *testing.T) {
	server := startTestServer(t)
	// Large enough that cancellation lands mid-copy.
	body := bytes.Repeat([]byte("x"), 8<<20)
	server.writeFile(t, "big.bin", body)
	conn := connect(t, server)

	conn.Engine().Start(transfer.Request{
		ID: 5, Direction: domain.Download,
		Source: server.path("big.bin"), Destination: filepath.Join(t.TempDir(), "big.bin"), Size: int64(len(body)),
	})
	conn.Engine().Cancel(5)

	if event := awaitTerminal(t, conn.Engine(), 5); event.Kind != transfer.Canceled {
		t.Fatalf("terminal event = %v (err %v), want Canceled", event.Kind, event.Err)
	}
}

func TestCloseEndsTheConnectionWithNoReason(t *testing.T) {
	server := startTestServer(t)
	conn, err := dialerFor(t, server).Dial(context.Background(), targetFor(server), session.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case reason := <-conn.Done():
		if reason != nil {
			t.Fatalf("Done reported %v for a Close the caller asked for, want nil", reason)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Done never fired after Close")
	}
	// Repeating Close must be safe, as the session.Conn contract requires.
	_ = conn.Close()
}

func TestServerGoingAwayReportsADrop(t *testing.T) {
	server := startTestServer(t)
	conn, err := dialerFor(t, server).Dial(context.Background(), targetFor(server), session.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	server.Close() // tears the connection down from the far end

	select {
	case reason := <-conn.Done():
		if reason == nil {
			t.Fatalf("a server going away must report a reason, not a clean close")
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Done never fired after the server went away")
	}
}

func TestEngineCloseIsSafeToRepeat(t *testing.T) {
	server := startTestServer(t)
	conn := connect(t, server)

	if err := conn.Engine().Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := conn.Engine().Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	for range conn.Engine().Events() {
	}
	// A Start after Close must not send on the closed channel.
	conn.Engine().Start(transfer.Request{ID: 99, Size: 10})
	_ = errors.New("")
}
