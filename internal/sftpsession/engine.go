package sftpsession

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/pkg/sftp"

	"tideftp/internal/domain"
	"tideftp/internal/transfer"
)

var _ transfer.Engine = (*Engine)(nil)

// progressInterval is how often a running transfer reports. Every chunk would
// flood the UI's event channel on a fast link.
const progressInterval = 200 * time.Millisecond

// copyChunk is the read/write size. 32KiB is a reasonable compromise for SFTP,
// which pipelines requests internally.
const copyChunk = 32 * 1024

// Engine moves bytes over the connection's sftp.Client. It runs whatever it is
// handed and never queues: the UI decides what starts and when.
type Engine struct {
	client *sftp.Client
	events chan transfer.Event
	quit   chan struct{}

	mu      sync.Mutex
	running map[int]chan struct{}
	closed  bool
	wg      sync.WaitGroup
}

func newEngine(client *sftp.Client) *Engine {
	return &Engine{
		client:  client,
		events:  make(chan transfer.Event, 64),
		quit:    make(chan struct{}),
		running: map[int]chan struct{}{},
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

// Close stops everything in flight and closes Events. quit is closed first so
// an emit waiting on a full buffer cannot deadlock the wait below.
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

// run performs one transfer and reports exactly one terminal event, which is
// what the transfer.Engine contract requires: without it the UI's queue would
// stall waiting for a slot to free up.
func (e *Engine) run(req transfer.Request, stop chan struct{}) {
	defer e.wg.Done()
	defer e.done(req.ID)

	sent, err := e.copy(req, stop)
	switch {
	case err == nil:
		e.emit(transfer.Event{ID: req.ID, Kind: transfer.Completed, BytesDone: sent})
	case errors.Is(err, errCanceled):
		e.emit(transfer.Event{ID: req.ID, Kind: transfer.Canceled, BytesDone: sent})
	default:
		e.emit(transfer.Event{ID: req.ID, Kind: transfer.Failed, BytesDone: sent, Err: err})
	}
}

var errCanceled = errors.New("canceled")

// copy opens both ends and streams between them. Destinations are truncated:
// resume and the rest of the conflict policy are not wired to the UI yet, so
// there is nothing here that could ask for anything else.
func (e *Engine) copy(req transfer.Request, stop chan struct{}) (int64, error) {
	src, dst, err := e.open(req)
	if err != nil {
		return 0, err
	}

	var closeOnce sync.Once
	closeBoth := func() {
		closeOnce.Do(func() {
			_ = src.Close()
			_ = dst.Close()
		})
	}
	finished := make(chan struct{})
	// LIFO: close(finished) retires the watcher first, then the handles go.
	defer closeBoth()
	defer close(finished)

	// Checking a channel between chunks only cancels a transfer that is still
	// moving. One parked on a dead connection needs its handles closed out
	// from under it, which is what this does.
	go func() {
		select {
		case <-stop:
		case <-e.quit:
		case <-finished:
			return
		}
		closeBoth()
	}()

	var sent int64
	buf := make([]byte, copyChunk)
	lastReport := time.Now()

	for {
		if canceled(stop, e.quit) {
			return sent, errCanceled
		}

		n, readErr := src.Read(buf)
		if n > 0 {
			written, writeErr := dst.Write(buf[:n])
			sent += int64(written)
			if writeErr != nil {
				if canceled(stop, e.quit) {
					return sent, errCanceled
				}
				return sent, fmt.Errorf("write %s: %w", req.Destination, writeErr)
			}
			if time.Since(lastReport) >= progressInterval {
				lastReport = time.Now()
				e.emit(transfer.Event{ID: req.ID, Kind: transfer.Progress, BytesDone: sent})
			}
		}
		if readErr == io.EOF {
			// Close the destination explicitly so an error flushing the last
			// bytes is reported instead of being swallowed by closeBoth.
			if err := dst.Close(); err != nil {
				return sent, fmt.Errorf("close %s: %w", req.Destination, err)
			}
			return sent, nil
		}
		if readErr != nil {
			// A read that fails because cancellation closed the handle is a
			// cancellation, not a transfer error.
			if canceled(stop, e.quit) {
				return sent, errCanceled
			}
			return sent, fmt.Errorf("read %s: %w", req.Source, readErr)
		}
	}
}

// canceled reports whether either channel has been closed, without blocking.
func canceled(stop, quit chan struct{}) bool {
	select {
	case <-stop:
		return true
	case <-quit:
		return true
	default:
		return false
	}
}

// open resolves the direction into a reader and a writer. Upload reads local
// disk and writes the server; download is the reverse.
func (e *Engine) open(req transfer.Request) (io.ReadCloser, io.WriteCloser, error) {
	if req.Direction == domain.Download {
		src, err := e.client.Open(req.Source)
		if err != nil {
			return nil, nil, fmt.Errorf("open %s: %w", req.Source, err)
		}
		if err := os.MkdirAll(filepath.Dir(req.Destination), 0o755); err != nil {
			src.Close()
			return nil, nil, fmt.Errorf("create %s: %w", filepath.Dir(req.Destination), err)
		}
		dst, err := os.Create(req.Destination)
		if err != nil {
			src.Close()
			return nil, nil, fmt.Errorf("create %s: %w", req.Destination, err)
		}
		return src, dst, nil
	}

	src, err := os.Open(req.Source)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", req.Source, err)
	}
	if dir := path.Dir(req.Destination); dir != "" && dir != "." {
		// A missing parent is not an error worth failing on by itself; the
		// create below reports it if it really is one.
		_ = e.client.MkdirAll(dir)
	}
	dst, err := e.client.Create(req.Destination)
	if err != nil {
		src.Close()
		return nil, nil, fmt.Errorf("create %s: %w", req.Destination, err)
	}
	return src, dst, nil
}

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
