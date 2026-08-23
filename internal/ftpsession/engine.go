package ftpsession

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jlaffaye/ftp"

	"tideftp/internal/domain"
	"tideftp/internal/transfer"
	"tideftp/internal/vfs"
)

var _ transfer.Engine = (*Engine)(nil)

// Engine moves bytes over borrowed control connections. Each running transfer
// holds one for its whole duration, which is why the pool's cap has to cover
// the UI's transfer parallelism as well as browsing.
//
// Starting, canceling, closing, and the exactly-one-terminal-event contract
// all live in transfer.Runner, shared with internal/sftpsession — this type
// is only the FTP-specific part: how to move one transfer's bytes over a
// pooled control connection.
type Engine struct {
	*transfer.Runner
	pool *pool
}

func newEngine(connections *pool) *Engine {
	e := &Engine{pool: connections}
	e.Runner = transfer.NewRunner(e.move)
	return e
}

func (e *Engine) move(req transfer.Request, stop, quit <-chan struct{}, report func(int64)) (int64, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stop:
		case <-quit:
		case <-ctx.Done():
		}
		cancel()
	}()

	conn, err := e.pool.get(ctx)
	if err != nil {
		if transfer.IsCanceled(stop, quit) {
			return 0, transfer.ErrCanceled
		}
		return 0, err
	}

	var sent int64
	if req.Direction == domain.Download {
		sent, err = e.download(conn, req, stop, quit, report)
	} else {
		sent, err = e.upload(conn, req, stop, quit, report)
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

func (e *Engine) download(conn *ftp.ServerConn, req transfer.Request, stop, quit <-chan struct{}, report func(int64)) (int64, error) {
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
		case <-quit:
		case <-finished:
			return
		}
		closeBoth()
	}()

	var sent int64
	buf := make([]byte, transfer.CopyChunk)
	lastReport := time.Now()
	for {
		if transfer.IsCanceled(stop, quit) {
			return sent, transfer.ErrCanceled
		}
		n, readErr := remote.Read(buf)
		if n > 0 {
			written, writeErr := local.Write(buf[:n])
			sent += int64(written)
			if writeErr != nil {
				return sent, fmt.Errorf("write %s: %w", req.Destination, writeErr)
			}
			if time.Since(lastReport) >= transfer.ProgressInterval {
				lastReport = time.Now()
				report(sent)
			}
		}
		if readErr == io.EOF {
			if err := local.Close(); err != nil {
				return sent, fmt.Errorf("close %s: %w", req.Destination, err)
			}
			return sent, nil
		}
		if readErr != nil {
			if transfer.IsCanceled(stop, quit) {
				return sent, transfer.ErrCanceled
			}
			return sent, fmt.Errorf("read %s: %w", source, readErr)
		}
	}
}

// upload hands Stor a reader rather than driving the copy itself: jlaffaye's
// Stor drains the reader and only returns when it is done. Progress and
// cancellation therefore live in the reader.
func (e *Engine) upload(conn *ftp.ServerConn, req transfer.Request, stop, quit <-chan struct{}, report func(int64)) (int64, error) {
	local, err := os.Open(req.Source)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", req.Source, err)
	}
	defer local.Close()

	destination := vfs.CleanRemote(req.Destination)
	if dir := path.Dir(destination); dir != "" && dir != "/" {
		makeDirAll(conn, dir)
	}

	reader := &progressReader{
		reader: local,
		stop:   stop,
		quit:   quit,
		report: report,
	}
	storErr := conn.Stor(destination, reader)
	if storErr != nil {
		if errors.Is(storErr, transfer.ErrCanceled) || transfer.IsCanceled(stop, quit) {
			return reader.sent, transfer.ErrCanceled
		}
		return reader.sent, fmt.Errorf("store %s: %w", destination, storErr)
	}
	return reader.sent, nil
}

// makeDirAll creates dir and every missing parent above it. MKD, unlike
// SFTP's MkdirAll, only ever creates the one directory named — it fails if
// its parent does not exist yet — so a destination nested under directories
// that do not exist needs each level created from the root down. A
// directory that already exists is not an error worth stopping for; Stor
// reports anything that is actually wrong with the final destination.
func makeDirAll(conn *ftp.ServerConn, dir string) {
	dir = strings.Trim(dir, "/")
	if dir == "" {
		return
	}
	current := ""
	for _, part := range strings.Split(dir, "/") {
		current += "/" + part
		_ = conn.MakeDir(current)
	}
}

// progressReader counts what Stor consumes, reports it no more often than
// transfer.ProgressInterval, and fails the read when the transfer is
// cancelled, which is what aborts Stor.
type progressReader struct {
	reader     io.Reader
	stop       <-chan struct{}
	quit       <-chan struct{}
	report     func(int64)
	sent       int64
	lastReport time.Time
}

func (p *progressReader) Read(buf []byte) (int, error) {
	if transfer.IsCanceled(p.stop, p.quit) {
		return 0, transfer.ErrCanceled
	}
	n, err := p.reader.Read(buf)
	if n > 0 {
		p.sent += int64(n)
		if time.Since(p.lastReport) >= transfer.ProgressInterval {
			p.lastReport = time.Now()
			p.report(p.sent)
		}
	}
	return n, err
}
