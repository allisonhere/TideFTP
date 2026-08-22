// Package faketransfer is the simulated transfer.Engine used for UI work
// before real FTP/FTPS/SFTP engines exist. It moves no bytes: it emits a
// plausible stream of progress events on a timer.
package faketransfer

import (
	"errors"
	"sync"
	"time"

	"tideftp/internal/transfer"
)

var _ transfer.Engine = (*Engine)(nil)

// DefaultInterval is how often a simulated transfer reports progress.
const DefaultInterval = 250 * time.Millisecond

// simulatedFailureEvery is how often the fake engine drops a transfer. The
// rule is deterministic on purpose: it keeps the Failed tab and the error
// styling reachable without real networking, and keeps tests repeatable.
// Delete this along with the rest of the package once real engines land.
const simulatedFailureEvery = 5

// errSimulated is the failure a dropped transfer reports.
var errSimulated = errors.New("connection reset by peer")

type Engine struct {
	events   chan transfer.Event
	interval time.Duration
	quit     chan struct{}

	mu      sync.Mutex
	running map[int]chan struct{}
	closed  bool
	wg      sync.WaitGroup
}

func New() *Engine { return NewWithInterval(DefaultInterval) }

// NewWithInterval builds an engine that steps at the given rate. Tests use a
// short interval; the app uses DefaultInterval.
func NewWithInterval(interval time.Duration) *Engine {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Engine{
		events:   make(chan transfer.Event, 64),
		interval: interval,
		quit:     make(chan struct{}),
		running:  map[int]chan struct{}{},
	}
}

func (e *Engine) Events() <-chan transfer.Event { return e.events }

func (e *Engine) Start(req transfer.Request) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	if _, busy := e.running[req.ID]; busy {
		e.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	e.running[req.ID] = stop
	e.wg.Add(1)
	e.mu.Unlock()

	go e.run(req, stop)
}

func (e *Engine) Cancel(id int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if stop, ok := e.running[id]; ok {
		close(stop)
		delete(e.running, id)
	}
}

// Close stops everything in flight and closes Events. Closing the quit channel
// first unblocks any emit that is waiting on a full buffer, so the wait below
// cannot deadlock against a UI that has stopped reading.
func (e *Engine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	close(e.quit)
	for id, stop := range e.running {
		close(stop)
		delete(e.running, id)
	}
	e.mu.Unlock()

	e.wg.Wait()
	close(e.events)
	return nil
}

func (e *Engine) run(req transfer.Request, stop chan struct{}) {
	defer e.wg.Done()
	defer e.done(req.ID)

	total := max(req.Size, 1)
	step := max(int64(48_000), total/12)
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	var sent int64
	for {
		select {
		case <-e.quit:
			return
		case <-stop:
			e.emit(transfer.Event{ID: req.ID, Kind: transfer.Canceled, BytesDone: sent})
			return
		case <-ticker.C:
			sent = min(sent+step, total)
			switch {
			case failsAt(req.ID, sent, total):
				e.emit(transfer.Event{ID: req.ID, Kind: transfer.Failed, BytesDone: sent, Err: errSimulated})
				return
			case sent >= total:
				e.emit(transfer.Event{ID: req.ID, Kind: transfer.Completed, BytesDone: total})
				return
			default:
				e.emit(transfer.Event{ID: req.ID, Kind: transfer.Progress, BytesDone: sent})
			}
		}
	}
}

// failsAt reports whether transfer id should drop at this point: every
// simulatedFailureEvery-th transfer fails once it is past the halfway mark.
func failsAt(id int, sent, total int64) bool {
	if id <= 0 || total <= 0 || id%simulatedFailureEvery != 0 {
		return false
	}
	return sent*2 >= total
}

// emit blocks until the event is taken or the engine is closing, so a stalled
// reader delays the simulation instead of dropping events on the floor.
func (e *Engine) emit(event transfer.Event) {
	select {
	case e.events <- event:
	case <-e.quit:
	}
}

func (e *Engine) done(id int) {
	e.mu.Lock()
	delete(e.running, id)
	e.mu.Unlock()
}
