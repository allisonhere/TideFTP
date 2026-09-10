package ftpsession

import (
	"bytes"
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"tideftp/internal/session"
)

// The UI dials inside a tea.Cmd that cancels the dial context as soon as the
// dial returns (see dialCmd in internal/ui): the context bounds *connecting*,
// not the session that follows. Every operation after connect must therefore
// survive that cancellation.
//
// This is not hypothetical. jlaffaye/ftp reuses the dial function it was given
// for every data connection as well as the control connection (openDataConn),
// so a dial function that closes over the connect context makes the first
// listing fail with "operation canceled" the instant the connection is up.
func TestOperationsSurviveTheDialContextBeingCanceled(t *testing.T) {
	server := startFTPServer(t)
	server.writeFile(t, "alpha.txt", []byte("a"))

	host, port, err := net.SplitHostPort(server.addr)
	if err != nil {
		t.Fatalf("split %q: %v", server.addr, err)
	}
	p, _ := strconv.Atoi(port)
	target := session.Target{Protocol: "ftp", Host: host, Port: p, User: ftpTestUser, StartPath: "/"}

	// Exactly what dialCmd does: a bounded context, cancelled the moment the
	// dial returns.
	dialCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	conn, err := New(Config{Timeout: 10 * time.Second}).
		Dial(dialCtx, target, session.Credentials{Password: ftpTestPass})
	if err != nil {
		cancel()
		t.Fatalf("Dial: %v", err)
	}
	cancel()
	t.Cleanup(func() { _ = conn.Close() })

	// A listing needs a data connection, which is where a dial function that
	// captured the connect context comes apart.
	entries, err := conn.FS().List(context.Background(), "/", true)
	if err != nil {
		t.Fatalf("List after the dial context was canceled: %v", err)
	}
	if !contains(names(entries), "alpha.txt") {
		t.Fatalf("List = %v, want alpha.txt", names(entries))
	}
}

// The greeting deadline bounds the control connection's greeting, and it is
// an absolute time. A data connection that inherited it would be cut off the
// moment that time passed — mid-transfer, on a connection the pool still
// considers healthy — so a read that straddles it has to keep working.
func TestATransferOutlivesTheGreetingDeadline(t *testing.T) {
	server := startFTPServer(t)
	body := bytes.Repeat([]byte("payload\n"), 4096)
	server.writeFile(t, "stream.bin", body)

	host, port, err := net.SplitHostPort(server.addr)
	if err != nil {
		t.Fatalf("split %q: %v", server.addr, err)
	}
	p, _ := strconv.Atoi(port)
	target := session.Target{Protocol: "ftp", Host: host, Port: p, User: ftpTestUser, StartPath: "/"}

	// Short enough that the pause below outlasts it.
	const deadline = 300 * time.Millisecond
	conn, err := New(Config{Timeout: deadline}).
		Dial(context.Background(), target, session.Credentials{Password: ftpTestPass})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	reader, err := conn.FS().Open(context.Background(), "/stream.bin")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reader.Close()

	head := make([]byte, 64)
	if _, err := io.ReadFull(reader, head); err != nil {
		t.Fatalf("read head: %v", err)
	}

	// Straddle the deadline with the data connection open and idle.
	time.Sleep(deadline * 2)

	rest := make([]byte, 64)
	if _, err := io.ReadFull(reader, rest); err != nil {
		t.Fatalf("read after the dial-time deadline passed: %v", err)
	}
	if !bytes.Equal(rest, body[64:128]) {
		t.Fatalf("rest = %q, want the next 64 bytes of the file", rest)
	}
}
