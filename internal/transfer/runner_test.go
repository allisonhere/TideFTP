package transfer

import (
	"errors"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

func awaitEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatalf("events closed before an event arrived")
		}
		return event
	case <-time.After(testTimeout):
		t.Fatalf("no event arrived")
	}
	return Event{}
}

func TestRunnerReportsProgressThenCompleted(t *testing.T) {
	move := func(req Request, stop, quit <-chan struct{}, report func(int64)) (int64, error) {
		report(50)
		return 100, nil
	}
	r := NewRunner(move)
	defer r.Close()

	r.Start(Request{ID: 1, Size: 100})

	if got := awaitEvent(t, r.Events()); got.Kind != Progress || got.BytesDone != 50 {
		t.Fatalf("first event = %+v, want Progress/50", got)
	}
	if got := awaitEvent(t, r.Events()); got.Kind != Completed || got.BytesDone != 100 {
		t.Fatalf("second event = %+v, want Completed/100", got)
	}
}

func TestRunnerReportsFailedWithTheMoveFuncsError(t *testing.T) {
	want := errors.New("disk full")
	move := func(req Request, stop, quit <-chan struct{}, report func(int64)) (int64, error) {
		return 40, want
	}
	r := NewRunner(move)
	defer r.Close()

	r.Start(Request{ID: 1})

	got := awaitEvent(t, r.Events())
	if got.Kind != Failed || got.BytesDone != 40 || !errors.Is(got.Err, want) {
		t.Fatalf("event = %+v, want Failed/40 wrapping %v", got, want)
	}
}

func TestRunnerCancelStopsAnInFlightTransferAsCanceled(t *testing.T) {
	started := make(chan struct{})
	move := func(req Request, stop, quit <-chan struct{}, report func(int64)) (int64, error) {
		close(started)
		<-stop
		return 10, ErrCanceled
	}
	r := NewRunner(move)
	defer r.Close()

	r.Start(Request{ID: 1})
	select {
	case <-started:
	case <-time.After(testTimeout):
		t.Fatalf("move never started")
	}
	r.Cancel(1)

	got := awaitEvent(t, r.Events())
	if got.Kind != Canceled || got.BytesDone != 10 {
		t.Fatalf("event = %+v, want Canceled/10", got)
	}
}

func TestRunnerIgnoresACancelForAnUnknownID(t *testing.T) {
	r := NewRunner(func(Request, <-chan struct{}, <-chan struct{}, func(int64)) (int64, error) {
		return 0, nil
	})
	defer r.Close()

	// Must not panic canceling a transfer nobody started.
	r.Cancel(999)
}

func TestRunnerStartIgnoresADuplicateID(t *testing.T) {
	block := make(chan struct{})
	calls := make(chan struct{}, 2)
	move := func(req Request, stop, quit <-chan struct{}, report func(int64)) (int64, error) {
		calls <- struct{}{}
		<-block
		return 0, nil
	}
	r := NewRunner(move)
	defer func() {
		close(block)
		r.Close()
	}()

	r.Start(Request{ID: 1})
	r.Start(Request{ID: 1}) // busy: must be a no-op, not a second goroutine

	select {
	case <-calls:
	case <-time.After(testTimeout):
		t.Fatalf("move never started")
	}
	select {
	case <-calls:
		t.Fatalf("move ran twice for a duplicate Start")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRunnerCloseStopsEverythingAndClosesEvents(t *testing.T) {
	move := func(req Request, stop, quit <-chan struct{}, report func(int64)) (int64, error) {
		select {
		case <-stop:
		case <-quit:
		}
		return 5, ErrCanceled
	}
	r := NewRunner(move)
	r.Start(Request{ID: 1})
	r.Start(Request{ID: 2})

	done := make(chan error, 1)
	go func() { done <- r.Close() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatalf("Close never returned; an in-flight transfer was not stopped")
	}

	// Events must close once Close has returned, not just stop accepting new
	// sends. Whether a terminal event for a transfer that was in flight
	// exactly at Close time gets through first is a best-effort race, not a
	// guarantee — emit's own select against quit can drop it either way.
	for range r.Events() {
	}

	// A second Close must not panic or block.
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestRunnerStartAfterCloseIsANoop(t *testing.T) {
	calls := 0
	move := func(req Request, stop, quit <-chan struct{}, report func(int64)) (int64, error) {
		calls++
		return 0, nil
	}
	r := NewRunner(move)
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r.Start(Request{ID: 1})
	time.Sleep(20 * time.Millisecond)
	if calls != 0 {
		t.Fatalf("move called after Close, want it never to run")
	}
}

func TestIsCanceledReportsEitherChannel(t *testing.T) {
	open := make(chan struct{})
	closedStop := make(chan struct{})
	close(closedStop)

	if IsCanceled(open, open) {
		t.Fatalf("IsCanceled true with neither channel closed")
	}
	if !IsCanceled(closedStop, open) {
		t.Fatalf("IsCanceled false with stop closed")
	}
	if !IsCanceled(open, closedStop) {
		t.Fatalf("IsCanceled false with quit closed")
	}
}
