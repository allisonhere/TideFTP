package sftpsession

import (
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

// Engine moves bytes over the connection's sftp.Client. It runs whatever it
// is handed and never queues: the UI decides what starts and when.
//
// Starting, canceling, closing, and the exactly-one-terminal-event contract
// all live in transfer.Runner, shared with internal/ftpsession — this type
// is only the SFTP-specific part: how to move one transfer's bytes.
type Engine struct {
	*transfer.Runner
	client *sftp.Client
}

func newEngine(client *sftp.Client) *Engine {
	e := &Engine{client: client}
	e.Runner = transfer.NewRunner(e.move)
	return e
}

// move opens both ends and streams between them. req.Offset is where both
// ends start: 0 for an ordinary full transfer, or the destination's current
// size when the conflict policy resolved to Resume — open (below) seeks both
// sides to it rather than truncating the destination.
func (e *Engine) move(req transfer.Request, stop, quit <-chan struct{}, report func(int64)) (int64, error) {
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
		case <-quit:
		case <-finished:
			return
		}
		closeBoth()
	}()

	sent := req.Offset
	buf := make([]byte, transfer.CopyChunk)
	lastReport := time.Now()

	for {
		if transfer.IsCanceled(stop, quit) {
			return sent, transfer.ErrCanceled
		}

		n, readErr := src.Read(buf)
		if n > 0 {
			written, writeErr := dst.Write(buf[:n])
			sent += int64(written)
			if writeErr != nil {
				if transfer.IsCanceled(stop, quit) {
					return sent, transfer.ErrCanceled
				}
				return sent, fmt.Errorf("write %s: %w", req.Destination, writeErr)
			}
			if time.Since(lastReport) >= transfer.ProgressInterval {
				lastReport = time.Now()
				report(sent)
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
			if transfer.IsCanceled(stop, quit) {
				return sent, transfer.ErrCanceled
			}
			return sent, fmt.Errorf("read %s: %w", req.Source, readErr)
		}
	}
}

// open resolves the direction into a reader and a writer. Upload reads local
// disk and writes the server; download is the reverse. Both ends are seeked
// to req.Offset when it's non-zero — the destination is opened without
// truncating so the bytes already there survive.
func (e *Engine) open(req transfer.Request) (io.ReadCloser, io.WriteCloser, error) {
	if req.Direction == domain.Download {
		src, err := e.client.Open(req.Source)
		if err != nil {
			return nil, nil, fmt.Errorf("open %s: %w", req.Source, err)
		}
		if req.Offset > 0 {
			if _, err := src.Seek(req.Offset, io.SeekStart); err != nil {
				src.Close()
				return nil, nil, fmt.Errorf("seek %s: %w", req.Source, err)
			}
		}
		if err := os.MkdirAll(filepath.Dir(req.Destination), 0o755); err != nil {
			src.Close()
			return nil, nil, fmt.Errorf("create %s: %w", filepath.Dir(req.Destination), err)
		}
		flags := os.O_WRONLY | os.O_CREATE
		if req.Offset == 0 {
			flags |= os.O_TRUNC
		}
		dst, err := os.OpenFile(req.Destination, flags, 0o644)
		if err != nil {
			src.Close()
			return nil, nil, fmt.Errorf("create %s: %w", req.Destination, err)
		}
		if req.Offset > 0 {
			if _, err := dst.Seek(req.Offset, io.SeekStart); err != nil {
				src.Close()
				dst.Close()
				return nil, nil, fmt.Errorf("seek %s: %w", req.Destination, err)
			}
		}
		return src, dst, nil
	}

	src, err := os.Open(req.Source)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", req.Source, err)
	}
	if req.Offset > 0 {
		if _, err := src.Seek(req.Offset, io.SeekStart); err != nil {
			src.Close()
			return nil, nil, fmt.Errorf("seek %s: %w", req.Source, err)
		}
	}
	if dir := path.Dir(req.Destination); dir != "" && dir != "." {
		// A missing parent is not an error worth failing on by itself; the
		// create below reports it if it really is one.
		_ = e.client.MkdirAll(dir)
	}
	var dst *sftp.File
	if req.Offset > 0 {
		dst, err = e.client.OpenFile(req.Destination, os.O_WRONLY|os.O_CREATE)
	} else {
		dst, err = e.client.Create(req.Destination)
	}
	if err != nil {
		src.Close()
		return nil, nil, fmt.Errorf("create %s: %w", req.Destination, err)
	}
	if req.Offset > 0 {
		if _, err := dst.Seek(req.Offset, io.SeekStart); err != nil {
			src.Close()
			dst.Close()
			return nil, nil, fmt.Errorf("seek %s: %w", req.Destination, err)
		}
	}
	return src, dst, nil
}
