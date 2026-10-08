package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"tideftp/internal/domain"
	"tideftp/internal/session"
	"tideftp/internal/transfer"
	"tideftp/internal/vfs"
)

// isTransient reports whether err looks like a lost or flaky connection — the
// kind a redial can fix — as opposed to a real answer from the server (no such
// file, permission denied, bad credentials) that would fail again identically.
var ftp421 = regexp.MustCompile(`(^|[^0-9])421([^0-9]|$)`)

func isTransient(err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, transfer.ErrCanceled), errors.Is(err, context.Canceled),
		errors.Is(err, fs.ErrNotExist), errors.Is(err, fs.ErrPermission),
		errors.Is(err, fs.ErrExist), errors.Is(err, vfs.ErrExists),
		errors.Is(err, vfs.ErrUnsupported), errors.Is(err, errUsage):
		return false
	}
	var untrusted *session.UntrustedHostKeyError
	if errors.As(err, &untrusted) {
		return false
	}
	var coded *codedError
	if errors.As(err, &coded) && (coded.code == exitAuth || coded.code == exitNotFound || coded.code == exitUsage) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"connection reset", "broken pipe", "connection lost", "connection refused",
		"use of closed", "unexpected eof", "i/o timeout", "connection closed",
		"eof", "no route to host", "network is unreachable",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	// 421 is FTP's "service not available, closing control connection".
	return ftp421.MatchString(msg)
}

// liveConn is a session.Conn that survives the connection dropping: when an
// operation or a transfer fails in a way a fresh connection could fix, it
// redials (with backoff) and carries on — resuming a partly moved file from
// where it stopped. It stands in for the plain connection, so no command
// needs to know it is there.
type liveConn struct {
	redial  func() (session.Conn, error)
	retries int // reconnects allowed per operation; <0 is unlimited
	sleep   func(time.Duration)
	logf    func(format string, args ...any)

	mu     sync.Mutex
	cur    session.Conn
	closed bool
	done   chan error
	base   time.Duration // first reconnect delay; zero means 1s

	fsw  *resilientFS
	engw *resilientEngine
}

func newLiveConn(first session.Conn, redial func() (session.Conn, error), retries int, sleep func(time.Duration), logf func(string, ...any)) *liveConn {
	if sleep == nil {
		sleep = time.Sleep
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	l := &liveConn{cur: first, redial: redial, retries: retries, sleep: sleep, logf: logf, done: make(chan error, 1)}
	l.fsw = &resilientFS{l: l}
	l.engw = newResilientEngine(l)
	return l
}

func (l *liveConn) FS() vfs.FS              { return l.fsw }
func (l *liveConn) Engine() transfer.Engine { return l.engw }
func (l *liveConn) Done() <-chan error      { return l.done }

func (l *liveConn) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	cur := l.cur
	l.cur = nil
	l.mu.Unlock()
	l.engw.close()
	l.done <- nil
	close(l.done)
	if cur != nil {
		return cur.Close()
	}
	return nil
}

// conn returns the live connection, dialling a new one if the last was dropped.
func (l *liveConn) conn() (session.Conn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, net.ErrClosed
	}
	if l.cur != nil {
		return l.cur, nil
	}
	c, err := l.redial()
	if err != nil {
		return nil, err
	}
	l.cur = c
	return c, nil
}

// drop discards bad (if it is still the current connection) so the next use
// dials afresh.
func (l *liveConn) drop(bad session.Conn) {
	l.mu.Lock()
	if l.cur == bad {
		l.cur = nil
	}
	l.mu.Unlock()
	if bad != nil {
		_ = bad.Close()
	}
}

// backoff is the pause before reconnect attempt n (0-based): 1s, 2s, 4s … 30s.
func backoff(n int) time.Duration { return backoffFrom(time.Second, n) }

func backoffFrom(base time.Duration, n int) time.Duration {
	return min(base<<min(n, 5), 30*time.Second)
}

// backoff is the pause before reconnect attempt n using this connection's base.
func (l *liveConn) backoff(n int) time.Duration {
	l.mu.Lock()
	base := l.base
	l.mu.Unlock()
	if base <= 0 {
		base = time.Second
	}
	return backoffFrom(base, n)
}

// setRetries changes the reconnect budget (`set net:max-retries`).
func (l *liveConn) setRetries(n int) {
	l.mu.Lock()
	l.retries = n
	l.mu.Unlock()
}

func (l *liveConn) maxRetries() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.retries
}

// setBackoffBase changes the first reconnect delay (`set net:reconnect-interval-base`).
func (l *liveConn) setBackoffBase(d time.Duration) {
	l.mu.Lock()
	l.base = d
	l.mu.Unlock()
}

// withRetry runs op on the current connection, redialling and repeating it
// while it fails transiently and attempts remain. attempt is 0 for the first
// try, so an op can treat a repeat specially.
func (l *liveConn) withRetry(ctx context.Context, what string, op func(c session.Conn, attempt int) error) error {
	for attempt := 0; ; attempt++ {
		c, err := l.conn()
		if err == nil {
			err = op(c, attempt)
			if err == nil {
				return nil
			}
			if !isTransient(err) || ctx.Err() != nil {
				return err
			}
			l.drop(c)
		} else if !isTransient(err) {
			return err
		}
		if r := l.maxRetries(); r >= 0 && attempt >= r {
			return err
		}
		wait := l.backoff(attempt)
		l.logf("%s: connection lost (%v); reconnecting in %s (%d/%s)", what, err, wait, attempt+1, retriesLabel(l.maxRetries()))
		l.sleep(wait)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func retriesLabel(n int) string {
	if n < 0 {
		return "∞"
	}
	return fmt.Sprint(n)
}

// resilientFS runs each vfs.FS call through withRetry. Calls that a repeat
// could have already completed (mkdir, remove, rename) treat the "already
// done" answer from a retry as success.
type resilientFS struct{ l *liveConn }

func (r *resilientFS) first() vfs.FS {
	// Pure path math only; any connection's FS answers it the same way.
	r.l.mu.Lock()
	defer r.l.mu.Unlock()
	if r.l.cur != nil {
		return r.l.cur.FS()
	}
	return pathOnlyFS{}
}

func (r *resilientFS) Child(current, name string) string { return r.first().Child(current, name) }
func (r *resilientFS) Parent(current string) string      { return r.first().Parent(current) }

func (r *resilientFS) List(ctx context.Context, dir string, hidden bool) (out []domain.Entry, err error) {
	err = r.l.withRetry(ctx, "list "+dir, func(c session.Conn, _ int) error {
		out, err = c.FS().List(ctx, dir, hidden)
		return err
	})
	return out, err
}

func (r *resilientFS) Stat(ctx context.Context, p string) (out domain.Entry, err error) {
	err = r.l.withRetry(ctx, "stat "+p, func(c session.Conn, _ int) error {
		out, err = c.FS().Stat(ctx, p)
		return err
	})
	return out, err
}

func (r *resilientFS) Mkdir(ctx context.Context, p string) error {
	return r.l.withRetry(ctx, "mkdir "+p, func(c session.Conn, attempt int) error {
		err := c.FS().Mkdir(ctx, p)
		if attempt > 0 && errors.Is(err, vfs.ErrExists) {
			return nil
		}
		return err
	})
}

func (r *resilientFS) Rename(ctx context.Context, from, to string) error {
	return r.l.withRetry(ctx, "rename "+from, func(c session.Conn, attempt int) error {
		err := c.FS().Rename(ctx, from, to)
		if attempt > 0 && errors.Is(err, fs.ErrNotExist) {
			if _, serr := c.FS().Stat(ctx, to); serr == nil {
				return nil // the lost attempt had already done it
			}
		}
		return err
	})
}

func (r *resilientFS) Remove(ctx context.Context, p string) error {
	return r.l.withRetry(ctx, "remove "+p, func(c session.Conn, attempt int) error {
		err := c.FS().Remove(ctx, p)
		if attempt > 0 && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	})
}

func (r *resilientFS) Chmod(ctx context.Context, p string, mode fs.FileMode) error {
	return r.l.withRetry(ctx, "chmod "+p, func(c session.Conn, _ int) error {
		return c.FS().Chmod(ctx, p, mode)
	})
}

// SetMtime lets sync preserve timestamps through the resilient wrapper; a
// backend that cannot is ErrUnsupported (vfs.MtimeSetter).
func (r *resilientFS) SetMtime(ctx context.Context, p string, mtime time.Time) error {
	return r.l.withRetry(ctx, "set time "+p, func(c session.Conn, _ int) error {
		setter, ok := c.FS().(vfs.MtimeSetter)
		if !ok {
			return fmt.Errorf("set time %s: %w", p, vfs.ErrUnsupported)
		}
		return setter.SetMtime(ctx, p, mtime)
	})
}

// Symlink and Readlink pass through to a backend that supports vfs.Symlinker.
func (r *resilientFS) Symlink(ctx context.Context, target, link string) error {
	return r.l.withRetry(ctx, "symlink "+link, func(c session.Conn, attempt int) error {
		l, ok := c.FS().(vfs.Symlinker)
		if !ok {
			return fmt.Errorf("symlink %s: %w", link, vfs.ErrUnsupported)
		}
		err := l.Symlink(ctx, target, link)
		if attempt > 0 && errors.Is(err, vfs.ErrExists) {
			return nil
		}
		return err
	})
}

func (r *resilientFS) Readlink(ctx context.Context, p string) (out string, err error) {
	err = r.l.withRetry(ctx, "readlink "+p, func(c session.Conn, _ int) error {
		l, ok := c.FS().(vfs.Symlinker)
		if !ok {
			return fmt.Errorf("readlink %s: %w", p, vfs.ErrUnsupported)
		}
		out, err = l.Readlink(ctx, p)
		return err
	})
	return out, err
}

func (r *resilientFS) ReadFile(ctx context.Context, p string) (out []byte, err error) {
	err = r.l.withRetry(ctx, "read "+p, func(c session.Conn, _ int) error {
		out, err = c.FS().ReadFile(ctx, p)
		return err
	})
	return out, err
}

func (r *resilientFS) Open(ctx context.Context, p string) (out io.ReadCloser, err error) {
	err = r.l.withRetry(ctx, "open "+p, func(c session.Conn, _ int) error {
		out, err = c.FS().Open(ctx, p)
		return err
	})
	return out, err
}

func (r *resilientFS) WriteFile(ctx context.Context, p string, data []byte) error {
	return r.l.withRetry(ctx, "write "+p, func(c session.Conn, _ int) error {
		return c.FS().WriteFile(ctx, p, data)
	})
}

// pathOnlyFS answers Child/Parent when no connection is up; it is never asked
// to do I/O because every I/O call goes through withRetry first.
type pathOnlyFS struct{ vfs.FS }

func (pathOnlyFS) Child(current, name string) string { return vfs.ChildRemote(current, name) }
func (pathOnlyFS) Parent(current string) string      { return vfs.ParentRemote(current) }

// resilientEngine is a transfer.Engine whose transfers survive the connection
// dropping. Each Start runs a worker that moves the file with transfer.Copy on
// the live connection and, if that fails transiently, redials, works out how
// much of the file already arrived, and continues from there.
type resilientEngine struct {
	l      *liveConn
	events chan transfer.Event

	mu      sync.Mutex
	cancels map[int]context.CancelFunc
	closed  bool
}

func newResilientEngine(l *liveConn) *resilientEngine {
	return &resilientEngine{l: l, events: make(chan transfer.Event, 64), cancels: map[int]context.CancelFunc{}}
}

func (e *resilientEngine) Events() <-chan transfer.Event { return e.events }

func (e *resilientEngine) Start(req transfer.Request) {
	ctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		cancel()
		e.emit(transfer.Event{ID: req.ID, Kind: transfer.Failed, Err: net.ErrClosed})
		return
	}
	e.cancels[req.ID] = cancel
	e.mu.Unlock()
	go func() {
		defer func() {
			e.mu.Lock()
			delete(e.cancels, req.ID)
			e.mu.Unlock()
			cancel()
		}()
		e.run(ctx, req)
	}()
}

func (e *resilientEngine) Cancel(id int) {
	e.mu.Lock()
	cancel := e.cancels[id]
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (e *resilientEngine) Close() error { e.close(); return nil }

func (e *resilientEngine) close() {
	e.mu.Lock()
	e.closed = true
	cancels := e.cancels
	e.cancels = map[int]context.CancelFunc{}
	e.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (e *resilientEngine) emit(ev transfer.Event) {
	e.events <- ev
}

func (e *resilientEngine) run(ctx context.Context, req transfer.Request) {
	cur := req
	lastDone := req.Offset
	for attempt := 0; ; attempt++ {
		conn, err := e.l.conn()
		if err == nil {
			err = transfer.Copy(ctx, conn.Engine(), cur, func(done int64) {
				lastDone = done
				e.emit(transfer.Event{ID: req.ID, Kind: transfer.Progress, BytesDone: done})
			})
			if err == nil {
				e.emit(transfer.Event{ID: req.ID, Kind: transfer.Completed, BytesDone: req.Size})
				return
			}
			if ctx.Err() != nil || errors.Is(err, transfer.ErrCanceled) {
				e.emit(transfer.Event{ID: req.ID, Kind: transfer.Canceled})
				return
			}
			if isTransient(err) {
				e.l.drop(conn)
			}
		}
		if r := e.l.maxRetries(); !isTransient(err) || (r >= 0 && attempt >= r) {
			e.emit(transfer.Event{ID: req.ID, Kind: transfer.Failed, Err: err})
			return
		}
		wait := e.l.backoff(attempt)
		e.l.logf("transfer %s: connection lost (%v); reconnecting in %s (%d/%s)", base(req), err, wait, attempt+1, retriesLabel(e.l.maxRetries()))
		e.l.sleep(wait)
		if ctx.Err() != nil {
			e.emit(transfer.Event{ID: req.ID, Kind: transfer.Canceled})
			return
		}
		next, rerr := e.l.resumePoint(ctx, req, lastDone)
		if rerr != nil {
			if r := e.l.maxRetries(); isTransient(rerr) && (r < 0 || attempt+1 < r) {
				// The reconnect itself failed; the loop will wait and try again.
				cur = req
				continue
			}
			e.emit(transfer.Event{ID: req.ID, Kind: transfer.Failed, Err: rerr})
			return
		}
		cur = req
		cur.Offset = next
		if req.Length > 0 {
			// A segment resumes inside its own range.
			end := req.Offset + req.Length
			if next >= end {
				e.emit(transfer.Event{ID: req.ID, Kind: transfer.Completed, BytesDone: end})
				return
			}
			cur.Length = end - next
		}
	}
}

func base(req transfer.Request) string {
	if req.Direction == domain.Download {
		return req.Source
	}
	return req.Destination
}

// resumePoint works out, on a fresh connection, how many bytes of req already
// sit at the destination. A download's partial file is local; an upload's is
// the remote destination. A source that changed size meanwhile cannot be
// continued and is reported as a permanent error rather than spliced.
func (l *liveConn) resumePoint(ctx context.Context, req transfer.Request, lastDone int64) (int64, error) {
	conn, err := l.conn()
	if err != nil {
		return 0, err
	}
	if req.Direction == domain.Download {
		if req.Size > 0 {
			st, err := conn.FS().Stat(ctx, req.Source)
			if err != nil {
				l.drop(conn)
				return 0, err
			}
			if st.Size != req.Size {
				return 0, fmt.Errorf("%s changed size during the transfer (%d → %d bytes); not resuming", req.Source, req.Size, st.Size)
			}
		}
		if req.Length > 0 {
			// The destination is a preallocated file shared by several
			// segments, so its size says nothing; the last progress report
			// is what this segment is known to have written.
			return max(lastDone, req.Offset), nil
		}
		info, err := os.Stat(req.Destination)
		if err != nil || (req.Size > 0 && info.Size() > req.Size) {
			return 0, nil
		}
		return info.Size(), nil
	}
	st, err := conn.FS().Stat(ctx, req.Destination)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		l.drop(conn)
		return 0, err
	}
	if req.Size > 0 && st.Size > req.Size {
		return 0, nil
	}
	return st.Size, nil
}
