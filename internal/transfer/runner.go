package transfer

import (
	"errors"
	"sync"
	"time"
)

// ProgressInterval is how often a running transfer should report progress,
// at most — every chunk would flood the UI's event channel on a fast link.
const ProgressInterval = 200 * time.Millisecond

// CopyChunk is a reasonable read/write buffer size for a MoveFunc's own copy
// loop.
const CopyChunk = 32 * 1024

// ErrCanceled marks a MoveFunc's returned error as a cancellation rather
// than a failure: Runner reports it as a Canceled event instead of Failed.
var ErrCanceled = errors.New("canceled")

// MoveFunc performs one transfer's actual byte movement. It must honor stop
// (this one transfer being canceled) and quit (the whole engine closing),
// and may call report to post progress — Runner does not throttle it, so a
// MoveFunc should not call report more often than is useful to show. A
// returned error wrapping ErrCanceled becomes a Canceled event rather than
// Failed; any other non-nil error becomes Failed.
type MoveFunc func(req Request, stop, quit <-chan struct{}, report func(sent int64)) (int64, error)

// Runner implements the bookkeeping every real Engine needs — starting,
// canceling, closing, and reporting exactly one terminal event per transfer,
// as the Engine contract requires — so a protocol package only has to write
// how to move the bytes, via MoveFunc.
type Runner struct {
	move   MoveFunc
	events chan Event
	quit   chan struct{}

	mu      sync.Mutex
	running map[int]chan struct{}
	closed  bool
	wg      sync.WaitGroup
}

var _ Engine = (*Runner)(nil)

// NewRunner builds a Runner that calls move for every transfer it is asked
// to Start.
func NewRunner(move MoveFunc) *Runner {
	return &Runner{
		move:    move,
		events:  make(chan Event, 64),
		quit:    make(chan struct{}),
		running: map[int]chan struct{}{},
	}
}

func (r *Runner) Events() <-chan Event { return r.events }

func (r *Runner) Start(req Request) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	if _, busy := r.running[req.ID]; busy {
		r.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	r.running[req.ID] = stop
	r.wg.Add(1)
	r.mu.Unlock()

	go r.run(req, stop)
}

func (r *Runner) Cancel(id int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if stop, ok := r.running[id]; ok {
		close(stop)
		delete(r.running, id)
	}
}

// Close stops everything in flight and closes Events. quit is closed first so
// an emit waiting on a full buffer cannot deadlock the wait below.
func (r *Runner) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	close(r.quit)
	for id, stop := range r.running {
		close(stop)
		delete(r.running, id)
	}
	r.mu.Unlock()

	r.wg.Wait()
	close(r.events)
	return nil
}

// run performs one transfer and reports exactly one terminal event, which is
// what the Engine contract requires: without it the caller's queue would
// stall waiting for a slot to free up.
func (r *Runner) run(req Request, stop chan struct{}) {
	defer r.wg.Done()
	defer r.done(req.ID)

	sent, err := r.move(req, stop, r.quit, func(sent int64) {
		r.emit(Event{ID: req.ID, Kind: Progress, BytesDone: sent})
	})
	switch {
	case err == nil:
		r.emit(Event{ID: req.ID, Kind: Completed, BytesDone: sent})
	case errors.Is(err, ErrCanceled):
		r.emit(Event{ID: req.ID, Kind: Canceled, BytesDone: sent})
	default:
		r.emit(Event{ID: req.ID, Kind: Failed, BytesDone: sent, Err: err})
	}
}

func (r *Runner) emit(event Event) {
	select {
	case r.events <- event:
	case <-r.quit:
	}
}

func (r *Runner) done(id int) {
	r.mu.Lock()
	delete(r.running, id)
	r.mu.Unlock()
}

// IsCanceled reports whether either channel has been closed, without
// blocking — the "should I stop now" check inside a MoveFunc's copy loop.
func IsCanceled(stop, quit <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	case <-quit:
		return true
	default:
		return false
	}
}
