package transfer

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stubEngine lets a test drive the exact event sequence Copy sees, including
// the failure and cancellation cases the fake engine's fixed rule cannot
// produce on demand.
type stubEngine struct {
	events   chan Event
	started  chan Request
	canceled chan int
}

func newStubEngine() *stubEngine {
	return &stubEngine{
		events:   make(chan Event, 8),
		started:  make(chan Request, 1),
		canceled: make(chan int, 1),
	}
}

func (s *stubEngine) Start(req Request)    { s.started <- req }
func (s *stubEngine) Cancel(id int)        { s.canceled <- id }
func (s *stubEngine) Events() <-chan Event { return s.events }
func (s *stubEngine) Close() error         { return nil }

func TestCopyReturnsWhenTransferCompletes(t *testing.T) {
	eng := newStubEngine()
	done := make(chan error, 1)
	go func() { done <- Copy(context.Background(), eng, Request{Size: 100}, nil) }()

	req := <-eng.started

	// Progress must not end the copy; only the terminal event does.
	eng.events <- Event{ID: req.ID, Kind: Progress, BytesDone: 40}
	eng.events <- Event{ID: req.ID, Kind: Completed, BytesDone: 100}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Copy returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Copy did not return after a Completed event")
	}
}

func TestCopyReportsAFailedTransfer(t *testing.T) {
	eng := newStubEngine()
	done := make(chan error, 1)
	go func() { done <- Copy(context.Background(), eng, Request{Size: 100}, nil) }()

	req := <-eng.started
	boom := errors.New("connection reset by peer")
	eng.events <- Event{ID: req.ID, Kind: Failed, Err: boom}

	if err := <-done; !errors.Is(err, boom) {
		t.Fatalf("Copy returned %v, want the transfer's own error", err)
	}
}

func TestCopyReportsProgress(t *testing.T) {
	eng := newStubEngine()
	var seen []int64
	done := make(chan error, 1)
	go func() {
		done <- Copy(context.Background(), eng, Request{Size: 100}, func(n int64) { seen = append(seen, n) })
	}()

	req := <-eng.started
	eng.events <- Event{ID: req.ID, Kind: Progress, BytesDone: 25}
	eng.events <- Event{ID: req.ID, Kind: Progress, BytesDone: 50}
	eng.events <- Event{ID: req.ID, Kind: Completed, BytesDone: 100}
	<-done

	if len(seen) != 2 || seen[0] != 25 || seen[1] != 50 {
		t.Fatalf("progress calls = %v, want [25 50]", seen)
	}
}

func TestCopyCancelsWhenContextIsDone(t *testing.T) {
	eng := newStubEngine()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Copy(ctx, eng, Request{Size: 1 << 30}, nil) }()

	req := <-eng.started
	cancel()

	// Copy must ask the engine to stop...
	select {
	case id := <-eng.canceled:
		if id != req.ID {
			t.Fatalf("Cancel(%d), want Cancel(%d)", id, req.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling the context did not cancel the transfer")
	}

	// ...and report the cancellation once the engine acknowledges it.
	eng.events <- Event{ID: req.ID, Kind: Canceled}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Copy returned %v, want context.Canceled", err)
	}
}
