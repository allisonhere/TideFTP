package sftpsession

import (
	"bytes"
	"context"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"tideftp/internal/domain"
	"tideftp/internal/session"
	"tideftp/internal/transfer"
)

// Live tests run against a real server rather than the in-process one. They
// are skipped unless TIDEFTP_TEST_SFTP_ADDR is set, so the suite stays green
// on a machine that cannot reach it.
//
//	TIDEFTP_TEST_SFTP_ADDR=host:port
//	TIDEFTP_TEST_SFTP_USER=name
//	TIDEFTP_TEST_SFTP_PASSWORD=secret     (or rely on an agent/key)
//	TIDEFTP_TEST_SFTP_PATH=/writable/dir
//	TIDEFTP_TEST_SFTP_KNOWN_HOSTS=/path/to/known_hosts
func liveConn(t *testing.T) (session.Conn, string) {
	t.Helper()
	address := os.Getenv("TIDEFTP_TEST_SFTP_ADDR")
	if address == "" {
		t.Skip("set TIDEFTP_TEST_SFTP_ADDR to run the live SFTP tests")
	}
	host, portText, ok := strings.Cut(address, ":")
	if !ok {
		t.Fatalf("TIDEFTP_TEST_SFTP_ADDR = %q, want host:port", address)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("bad port in %q: %v", address, err)
	}
	remoteDir := os.Getenv("TIDEFTP_TEST_SFTP_PATH")
	if remoteDir == "" {
		remoteDir = "/"
	}

	dialer := New(Config{
		KnownHostsPath: os.Getenv("TIDEFTP_TEST_SFTP_KNOWN_HOSTS"),
		Password:       os.Getenv("TIDEFTP_TEST_SFTP_PASSWORD"),
		UseAgent:       true,
		Timeout:        15 * time.Second,
	})
	conn, err := dialer.Dial(context.Background(), session.Target{
		Protocol: "sftp", Host: host, Port: port,
		User: os.Getenv("TIDEFTP_TEST_SFTP_USER"), StartPath: remoteDir,
	}, session.Credentials{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, remoteDir
}

func TestLiveListAndRoundTrip(t *testing.T) {
	conn, remoteDir := liveConn(t)

	if _, err := conn.FS().List(context.Background(), remoteDir, false); err != nil {
		t.Fatalf("List %s: %v", remoteDir, err)
	}

	body := bytes.Repeat([]byte("tideftp live sftp\n"), 4000)
	local := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(local, body, 0o644); err != nil {
		t.Fatal(err)
	}
	remote := path.Join(remoteDir, "tideftp-live-test.bin")

	conn.Engine().Start(transfer.Request{
		ID: 1, Direction: domain.Upload,
		Source: local, Destination: remote, Size: int64(len(body)),
	})
	if event := awaitTerminal(t, conn.Engine(), 1); event.Kind != transfer.Completed {
		t.Fatalf("upload ended as %v (err %v)", event.Kind, event.Err)
	}

	// The uploaded file must now appear in a listing.
	entries, err := conn.FS().List(context.Background(), remoteDir, false)
	if err != nil {
		t.Fatalf("List after upload: %v", err)
	}
	var found bool
	for _, entry := range entries {
		if entry.Name == "tideftp-live-test.bin" {
			found = true
			if entry.Size != int64(len(body)) {
				t.Errorf("listed size %d, want %d", entry.Size, len(body))
			}
		}
	}
	if !found {
		t.Fatalf("uploaded file is not in the listing")
	}

	back := filepath.Join(t.TempDir(), "roundtrip.bin")
	conn.Engine().Start(transfer.Request{
		ID: 2, Direction: domain.Download,
		Source: remote, Destination: back, Size: int64(len(body)),
	})
	if event := awaitTerminal(t, conn.Engine(), 2); event.Kind != transfer.Completed {
		t.Fatalf("download ended as %v (err %v)", event.Kind, event.Err)
	}
	got, err := os.ReadFile(back)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("round trip differs: %d bytes back, %d sent", len(got), len(body))
	}
}

func TestLiveRejectsAWrongHostKey(t *testing.T) {
	if os.Getenv("TIDEFTP_TEST_SFTP_ADDR") == "" {
		t.Skip("set TIDEFTP_TEST_SFTP_ADDR to run the live SFTP tests")
	}
	address := os.Getenv("TIDEFTP_TEST_SFTP_ADDR")
	host, portText, _ := strings.Cut(address, ":")
	port, _ := strconv.Atoi(portText)

	// A known_hosts naming a key this server does not have.
	server := startTestServer(t)
	dialer := New(Config{
		KnownHostsPath: server.knownHostsFile(t),
		Password:       os.Getenv("TIDEFTP_TEST_SFTP_PASSWORD"),
		Timeout:        15 * time.Second,
	})
	if _, err := dialer.Dial(context.Background(), session.Target{
		Protocol: "sftp", Host: host, Port: port, User: os.Getenv("TIDEFTP_TEST_SFTP_USER"),
	}, session.Credentials{}); err == nil {
		t.Fatalf("connected to a live server whose host key is not in known_hosts")
	}
}
