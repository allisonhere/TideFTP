package faketransfer

import (
	"testing"
	"time"

	"tideftp/internal/domain"
	"tideftp/internal/transfer"
)

const testInterval = time.Millisecond

func request(id int, size int64) transfer.Request {
	return transfer.Request{ID: id, Direction: domain.Upload, Source: "/a", Destination: "/b", Size: size}
}

// drain reads events for one transfer until its terminal event arrives.
func drain(t *testing.T, engine *Engine, id int) transfer.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event, ok := <-engine.Events():
			if !ok {
				t.Fatalf("event stream closed before transfer %d finished", id)
			}
			if event.ID == id && event.Terminal() {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for transfer %d to finish", id)
		}
	}
}

func TestTransferCompletes(t *testing.T) {
	engine := NewWithInterval(testInterval)
	defer engine.Close()

	engine.Start(request(1, 500_000))
	event := drain(t, engine, 1)

	if event.Kind != transfer.Completed {
		t.Fatalf("terminal event = %v, want Completed", event.Kind)
	}
	if event.BytesDone != 500_000 {
		t.Fatalf("bytes done = %d, want the full 500000", event.BytesDone)
	}
}

func TestEveryNthTransferFails(t *testing.T) {
	engine := NewWithInterval(testInterval)
	defer engine.Close()

	// simulatedFailureEvery is the rule that keeps the Failed tab reachable.
	engine.Start(request(simulatedFailureEvery, 500_000))
	event := drain(t, engine, simulatedFailureEvery)
	if event.Kind != transfer.Failed {
		t.Fatalf("transfer %d ended as %v, want Failed", simulatedFailureEvery, event.Kind)
	}
	if event.Err == nil {
		t.Fatalf("a failed transfer must report why")
	}

	engine.Start(request(simulatedFailureEvery+1, 500_000))
	if event := drain(t, engine, simulatedFailureEvery+1); event.Kind != transfer.Completed {
		t.Fatalf("transfer %d ended as %v, want Completed", simulatedFailureEvery+1, event.Kind)
	}
}

func TestCancelStopsATransfer(t *testing.T) {
	engine := NewWithInterval(10 * time.Millisecond)
	defer engine.Close()

	// Large enough that it cannot finish before the cancel lands.
	engine.Start(request(1, 1<<30))
	engine.Cancel(1)

	if event := drain(t, engine, 1); event.Kind != transfer.Canceled {
		t.Fatalf("terminal event = %v, want Canceled", event.Kind)
	}
}

func TestCancelUnknownIDIsANoop(t *testing.T) {
	engine := NewWithInterval(testInterval)
	defer engine.Close()
	engine.Cancel(404) // must not panic
}

func TestCloseStopsEverythingAndClosesTheStream(t *testing.T) {
	engine := NewWithInterval(10 * time.Millisecond)
	for id := 1; id <= 5; id++ {
		engine.Start(request(id, 1<<30))
	}

	if err := engine.Close(); err != nil {
		t.Fatalf("Close returned %v", err)
	}
	if err := engine.Close(); err != nil {
		t.Fatalf("second Close returned %v, want it to be safe to repeat", err)
	}

	// Draining must terminate: the channel is closed once every goroutine is done.
	for range engine.Events() {
	}

	// A late Start after Close must not send on the closed channel.
	engine.Start(request(99, 1000))
}

// TestCloseWithNoReaderDoesNotDeadlock covers the case the emit/quit handshake
// exists for: the UI stops reading events while transfers are still running.
func TestCloseWithNoReaderDoesNotDeadlock(t *testing.T) {
	engine := NewWithInterval(time.Millisecond)
	for id := 1; id <= 200; id++ {
		engine.Start(request(id, 1<<30))
	}
	time.Sleep(20 * time.Millisecond) // let the event buffer fill up

	done := make(chan struct{})
	go func() {
		engine.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Close deadlocked with a full event buffer and no reader")
	}
}
