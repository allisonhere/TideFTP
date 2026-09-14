package netcheck

import (
	"context"
	"net"
	"testing"
	"time"
)

// linkUp returns a link check that always reports a usable interface, so the
// dial branches are tested independently of this machine's networking.
func linkUp() bool { return true }

// closedPort returns a port nothing is listening on: bind a listener, read its
// port, then close it. Connecting there is refused rather than timed out, so
// the test stays fast.
func closedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return port
}

func TestProbeReachableAgainstLiveListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port

	result := probe(context.Background(), "127.0.0.1", port, time.Second, linkUp)
	if result.Status != Reachable {
		t.Fatalf("probe = %v (%q), want reachable", result.Status, result.Detail)
	}
}

func TestProbeHostUnreachableWhenNothingListens(t *testing.T) {
	port := closedPort(t)

	result := probe(context.Background(), "127.0.0.1", port, time.Second, linkUp)
	if result.Status != HostUnreachable {
		t.Fatalf("probe = %v (%q), want host unreachable", result.Status, result.Detail)
	}
}

// TestProbeNoLinkSkipsTheDial pins the distinction the UI relies on: with no
// link, the server is never blamed and the status names the local problem.
// The closed port is the tell — if the dial had run, it would have refused
// and reported host unreachable instead.
func TestProbeNoLinkSkipsTheDial(t *testing.T) {
	result := probe(context.Background(), "127.0.0.1", closedPort(t), time.Second, func() bool {
		return false
	})
	if result.Status != NoLink {
		t.Fatalf("probe = %v, want no link", result.Status)
	}
	if result.Detail != "no network connection" {
		t.Fatalf("detail = %q, want it to name the local network", result.Detail)
	}
}

func TestProbeHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := probe(ctx, "127.0.0.1", closedPort(t), time.Second, linkUp)
	if result.Status != HostUnreachable {
		t.Fatalf("probe = %v, want host unreachable for a cancelled dial", result.Status)
	}
}

func TestStatusString(t *testing.T) {
	cases := map[Status]string{
		Reachable:       "reachable",
		HostUnreachable: "host unreachable",
		NoLink:          "no link",
	}
	for status, want := range cases {
		if got := status.String(); got != want {
			t.Errorf("Status(%d).String() = %q, want %q", int(status), got, want)
		}
	}
	if got := Status(99).String(); got != "status(99)" {
		t.Errorf("unknown status = %q, want a numbered fallback", got)
	}
}
