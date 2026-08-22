package fakesession

import (
	"context"
	"errors"
	"testing"
	"time"

	"tideftp/internal/session"
)

var target = targetFor("demo.local")

func targetFor(host string) session.Target {
	return session.Target{Host: host, Protocol: "sftp", User: "allie"}
}

func TestDialSucceedsForAKnownHost(t *testing.T) {
	conn, err := New(0, 0, "demo.local").Dial(context.Background(), target)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	if conn.FS() == nil || conn.Engine() == nil {
		t.Fatalf("a live connection must expose both an FS and an engine")
	}
}

func TestDialFailsForAnUnknownHost(t *testing.T) {
	if _, err := New(0, 0, "demo.local").Dial(context.Background(), targetFor("nope.invalid")); err == nil {
		t.Fatalf("dialing an unknown host returned no error")
	}
}

func TestDialHonoursContextCancellation(t *testing.T) {
	dialer := New(10*time.Second, 0, "demo.local")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := dialer.Dial(ctx, target); err == nil {
		t.Fatalf("a cancelled dial returned no error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancellation took %v; dial latency should be interruptible", elapsed)
	}
}

func TestCloseReportsNoReasonAndIsRepeatable(t *testing.T) {
	conn, err := New(0, 0, "demo.local").Dial(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	select {
	case reason := <-conn.Done():
		if reason != nil {
			t.Fatalf("Done reported %v for a Close the caller asked for, want nil", reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Done never fired after Close")
	}
}

func TestDropReportsTheReason(t *testing.T) {
	conn, err := New(0, 0, "demo.local").Dial(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("server went away")
	conn.(*Conn).Drop(want)

	select {
	case reason := <-conn.Done():
		if !errors.Is(reason, want) {
			t.Fatalf("Done reported %v, want %v", reason, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Done never fired after Drop")
	}

	// A Close after a drop must not change the reason or panic.
	if err := conn.Close(); err != nil {
		t.Fatalf("Close after Drop: %v", err)
	}
}

func TestDropClosesTheEngine(t *testing.T) {
	conn, err := New(0, 0, "demo.local").Dial(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	conn.(*Conn).Drop(errors.New("gone"))

	// The engine's stream must end, or the UI's pump would park forever.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-conn.Engine().Events():
			if !ok {
				return
			}
		case <-deadline:
			t.Fatalf("engine stream stayed open after the connection dropped")
		}
	}
}
