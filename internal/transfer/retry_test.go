package transfer

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunnerRetriesTransientFailureBeforeAnyData(t *testing.T) {
	var calls atomic.Int32
	r := NewRunner(func(Request, <-chan struct{}, <-chan struct{}, func(int64)) (int64, error) {
		if calls.Add(1) == 1 {
			return 0, errors.New("421 too many connections")
		}
		return 5, nil
	})
	defer r.Close()
	r.Start(Request{ID: 1})
	select {
	case ev := <-r.Events():
		if ev.Kind != Completed || calls.Load() != 2 {
			t.Fatalf("event %+v after %d calls, want Completed after 2", ev, calls.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
}

func TestRunnerDoesNotRetryPermanentFailure(t *testing.T) {
	var calls atomic.Int32
	r := NewRunner(func(Request, <-chan struct{}, <-chan struct{}, func(int64)) (int64, error) {
		calls.Add(1)
		return 0, errors.New("550 no such file")
	})
	defer r.Close()
	r.Start(Request{ID: 1})
	ev := <-r.Events()
	if ev.Kind != Failed || calls.Load() != 1 {
		t.Fatalf("event %+v after %d calls, want Failed after 1", ev, calls.Load())
	}
}
