package ftpsession

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

// Live tests run against a real FTP server. They are skipped unless
// TIDEFTP_TEST_FTP_ADDR is set, so the suite stays green on a machine that
// cannot reach one.
//
//	TIDEFTP_TEST_FTP_ADDR=host:port
//	TIDEFTP_TEST_FTP_USER=name
//	TIDEFTP_TEST_FTP_PASSWORD=secret
//	TIDEFTP_TEST_FTP_PATH=/writable/dir
func liveConn(t *testing.T) (session.Conn, string) {
	t.Helper()
	address := os.Getenv("TIDEFTP_TEST_FTP_ADDR")
	if address == "" {
		t.Skip("set TIDEFTP_TEST_FTP_ADDR to run the live FTP tests")
	}
	host, portText, ok := strings.Cut(address, ":")
	if !ok {
		t.Fatalf("TIDEFTP_TEST_FTP_ADDR = %q, want host:port", address)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("bad port in %q: %v", address, err)
	}
	remoteDir := os.Getenv("TIDEFTP_TEST_FTP_PATH")
	if remoteDir == "" {
		remoteDir = "/"
	}

	conn, err := New(Config{
		Password: os.Getenv("TIDEFTP_TEST_FTP_PASSWORD"),
		Timeout:  15 * time.Second,
	}).Dial(context.Background(), session.Target{
		Protocol: "ftp", Host: host, Port: port,
		User: os.Getenv("TIDEFTP_TEST_FTP_USER"), StartPath: remoteDir,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, remoteDir
}

func awaitTerminal(t *testing.T, engine transfer.Engine, id int) transfer.Event {
	t.Helper()
	deadline := time.After(60 * time.Second)
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

func TestLiveListAndRoundTrip(t *testing.T) {
	conn, remoteDir := liveConn(t)

	if _, err := conn.FS().List(context.Background(), remoteDir, false); err != nil {
		t.Fatalf("List %s: %v", remoteDir, err)
	}

	body := bytes.Repeat([]byte("tideftp live ftp\n"), 4000)
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

// TestLiveParallelTransfersShareThePool is the case the pool exists for: FTP
// control connections carry one command at a time, so concurrent transfers
// must each get their own.
func TestLiveParallelTransfersShareThePool(t *testing.T) {
	conn, remoteDir := liveConn(t)

	body := bytes.Repeat([]byte("parallel\n"), 20000)
	local := filepath.Join(t.TempDir(), "parallel.bin")
	if err := os.WriteFile(local, body, 0o644); err != nil {
		t.Fatal(err)
	}

	const count = 3
	for id := 1; id <= count; id++ {
		conn.Engine().Start(transfer.Request{
			ID: id, Direction: domain.Upload,
			Source:      local,
			Destination: path.Join(remoteDir, "tideftp-parallel-"+strconv.Itoa(id)+".bin"),
			Size:        int64(len(body)),
		})
	}
	// Parallel transfers finish in whatever order the server manages, so every
	// terminal event has to be collected in one pass. Waiting for them one id
	// at a time throws away the events that arrive early.
	for _, event := range awaitAllTerminal(t, conn.Engine(), count) {
		if event.Kind != transfer.Completed {
			t.Fatalf("transfer %d ended as %v (err %v)", event.ID, event.Kind, event.Err)
		}
	}
}

// awaitAllTerminal drains events until every transfer 1..count has reported.
func awaitAllTerminal(t *testing.T, engine transfer.Engine, count int) map[int]transfer.Event {
	t.Helper()
	seen := make(map[int]transfer.Event, count)
	deadline := time.After(120 * time.Second)
	for len(seen) < count {
		select {
		case event, ok := <-engine.Events():
			if !ok {
				t.Fatalf("engine closed its stream with %d of %d transfers finished", len(seen), count)
			}
			if event.Terminal() {
				seen[event.ID] = event
			}
		case <-deadline:
			t.Fatalf("timed out with %d of %d transfers finished", len(seen), count)
		}
	}
	return seen
}

func TestLiveWrongPasswordFails(t *testing.T) {
	address := os.Getenv("TIDEFTP_TEST_FTP_ADDR")
	if address == "" {
		t.Skip("set TIDEFTP_TEST_FTP_ADDR to run the live FTP tests")
	}
	host, portText, _ := strings.Cut(address, ":")
	port, _ := strconv.Atoi(portText)

	_, err := New(Config{Password: "definitely-not-the-password", Timeout: 15 * time.Second}).
		Dial(context.Background(), session.Target{
			Protocol: "ftp", Host: host, Port: port, User: os.Getenv("TIDEFTP_TEST_FTP_USER"),
		})
	if err == nil {
		t.Fatalf("connected with a wrong password")
	}
	if !strings.Contains(err.Error(), "login") {
		t.Fatalf("error = %v, want it to name the login failure", err)
	}
}
