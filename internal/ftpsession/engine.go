package ftpsession

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/jlaffaye/ftp"

	"tideftp/internal/domain"
	"tideftp/internal/transfer"
	"tideftp/internal/vfs"
)

var _ transfer.Engine = (*Engine)(nil)

const (
	progressInterval = 200 * time.Millisecond
	copyChunk        = 32 * 1024
)

var errCanceled = errors.New("canceled")

// Engine moves bytes over borrowed control connections. Each running transfer
// holds one for its whole duration, which is why the pool's cap has to cover
// the UI's transfer parallelism as well as browsing.
type Engine struct {
	pool   *pool
	events chan transfer.Event
	quit   chan struct{}

	mu      sync.Mutex
	running map[int]chan struct{}
	closed  bool
	wg      sync.WaitGroup
}

func newEngine(connections *pool) *Engine {
	return &Engine{
		pool:    connections,
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

// run performs one transfer and reports exactly one terminal event, as the
// transfer.Engine contract requires.
func (e *Engine) run(req transfer.Request, stop chan struct{}) {
	defer e.wg.Done()
	defer e.done(req.ID)

	sent, err := e.move(req, stop)
	switch {
	case err == nil:
		e.emit(transfer.Event{ID: req.ID, Kind: transfer.Completed, BytesDone: sent})
	case errors.Is(err, errCanceled):
		e.emit(transfer.Event{ID: req.ID, Kind: transfer.Canceled, BytesDone: sent})
	default:
		e.emit(transfer.Event{ID: req.ID, Kind: transfer.Failed, BytesDone: sent, Err: err})
	}
}

func (e *Engine) move(req transfer.Request, stop chan struct{}) (int64, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stop:
		case <-e.quit:
		case <-ctx.Done():
		}
		cancel()
	}()

	conn, err := e.pool.get(ctx)
	if err != nil {
		if canceled(stop, e.quit) {
			return 0, errCanceled
		}
		return 0, err
	}

	var sent int64
	if req.Direction == domain.Download {
		sent, err = e.download(conn, req, stop)
	} else {
		sent, err = e.upload(conn, req, stop)
	}

	// A transfer that failed or was cancelled may have left the control
	// connection mid-response, so it is dropped rather than reused.
	if err != nil {
		e.pool.discard(conn)
	} else {
		e.pool.put(conn)
	}
	return sent, err
}

func (e *Engine) download(conn *ftp.ServerConn, req transfer.Request, stop chan struct{}) (int64, error) {
	source := vfs.CleanRemote(req.Source)
	remote, err := conn.Retr(source)
	if err != nil {
		return 0, fmt.Errorf("retrieve %s: %w", source, err)
	}
	defer remote.Close()

	if err := os.MkdirAll(filepath.Dir(req.Destination), 0o755); err != nil {
		return 0, fmt.Errorf("create %s: %w", filepath.Dir(req.Destination), err)
	}
	local, err := os.Create(req.Destination)
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", req.Destination, err)
	}

	var closeOnce sync.Once
	closeBoth := func() {
		closeOnce.Do(func() {
			_ = remote.Close()
			_ = local.Close()
		})
	}
	finished := make(chan struct{})
	defer closeBoth()
	defer close(finished)

	// Closing the handles is the only way to unblock a read parked on a dead
	// connection; checking a channel between chunks only stops one still moving.
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
		n, readErr := remote.Read(buf)
		if n > 0 {
			written, writeErr := local.Write(buf[:n])
			sent += int64(written)
			if writeErr != nil {
				return sent, fmt.Errorf("write %s: %w", req.Destination, writeErr)
			}
			if time.Since(lastReport) >= progressInterval {
				lastReport = time.Now()
				e.emit(transfer.Event{ID: req.ID, Kind: transfer.Progress, BytesDone: sent})
			}
		}
		if readErr == io.EOF {
			if err := local.Close(); err != nil {
				return sent, fmt.Errorf("close %s: %w", req.Destination, err)
			}
			return sent, nil
		}
		if readErr != nil {
			if canceled(stop, e.quit) {
				return sent, errCanceled
			}
			return sent, fmt.Errorf("read %s: %w", source, readErr)
		}
	}
}

// upload hands Stor a reader rather than driving the copy itself: jlaffaye's
// Stor drains the reader and only returns when it is done. Progress and
// cancellation therefore live in the reader.
func (e *Engine) upload(conn *ftp.ServerConn, req transfer.Request, stop chan struct{}) (int64, error) {
	local, err := os.Open(req.Source)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", req.Source, err)
	}
	defer local.Close()

	destination := vfs.CleanRemote(req.Destination)
	if dir := path.Dir(destination); dir != "" && dir != "/" {
		// A missing parent is not fatal by itself; Stor reports it if it is.
		_ = conn.MakeDir(dir)
	}

	reader := &progressReader{
		reader: local,
		stop:   stop,
		quit:   e.quit,
		report: func(sent int64) {
			e.emit(transfer.Event{ID: req.ID, Kind: transfer.Progress, BytesDone: sent})
		},
	}
	storErr := conn.Stor(destination, reader)
	if storErr != nil {
		if errors.Is(storErr, errCanceled) || canceled(stop, e.quit) {
			return reader.sent, errCanceled
		}
		return reader.sent, fmt.Errorf("store %s: %w", destination, storErr)
	}
	return reader.sent, nil
}

// progressReader counts what Stor consumes, reports it no more often than
// progressInterval, and fails the read when the transfer is cancelled, which
// is what aborts Stor.
type progressReader struct {
	reader     io.Reader
	stop       chan struct{}
	quit       chan struct{}
	report     func(int64)
	sent       int64
	lastReport time.Time
}

func (p *progressReader) Read(buf []byte) (int, error) {
	if canceled(p.stop, p.quit) {
		return 0, errCanceled
	}
	n, err := p.reader.Read(buf)
	if n > 0 {
		p.sent += int64(n)
		if time.Since(p.lastReport) >= progressInterval {
			p.lastReport = time.Now()
			p.report(p.sent)
		}
	}
	return n, err
}

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
