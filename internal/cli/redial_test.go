package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"testing"
	"time"

	"tideftp/internal/domain"
	"tideftp/internal/session"
	"tideftp/internal/transfer"
	"tideftp/internal/vfs"
)

func TestIsTransient(t *testing.T) {
	yes := []error{
		io.EOF, io.ErrUnexpectedEOF, net.ErrClosed,
		fmt.Errorf("read /x: %w", io.ErrUnexpectedEOF),
		errors.New("write tcp 1.2.3.4:5->6.7.8.9:22: write: broken pipe"),
		errors.New("sftp: connection lost"),
		errors.New("421 Service not available, closing control connection"),
		&net.OpError{Op: "read", Err: errors.New("connection reset by peer")},
		&codedError{code: exitConnect, err: errors.New("dial tcp: connection refused")},
	}
	for _, err := range yes {
		if !isTransient(err) {
			t.Errorf("isTransient(%v) = false, want true", err)
		}
	}
	no := []error{
		nil, fs.ErrNotExist, fmt.Errorf("stat /x: %w", fs.ErrNotExist), fs.ErrPermission,
		vfs.ErrExists, vfs.ErrUnsupported, transfer.ErrCanceled, context.Canceled,
		usageError("nope"),
		&codedError{code: exitAuth, err: errors.New("unable to authenticate")},
		errors.New("550 /file4210.txt: No such file or directory"),
		errors.New("530 Login incorrect"),
	}
	for _, err := range no {
		if isTransient(err) {
			t.Errorf("isTransient(%v) = true, want false", err)
		}
	}
}

func TestBackoffSequence(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		if got := backoff(i); got != w {
			t.Errorf("backoff(%d) = %v, want %v", i, got, w)
		}
	}
}

// flakyConn fails its first n List calls with a transient error, then works.
type flakyFS struct {
	vfs.FS
	failures *int
}

func (f flakyFS) List(ctx context.Context, d string, h bool) ([]domain.Entry, error) {
	if *f.failures > 0 {
		*f.failures--
		return nil, io.ErrUnexpectedEOF
	}
	return f.FS.List(ctx, d, h)
}

type flakyConn struct {
	session.Conn
	fs vfs.FS
}

func (c flakyConn) FS() vfs.FS { return c.fs }

func TestLiveConnRedialsAndGivesUp(t *testing.T) {
	remote := fakefsRemote()
	failures := 2
	dials := 0
	redial := func() (session.Conn, error) {
		dials++
		return flakyConn{Conn: &fakeConn{fs: remote, eng: newCopyEngine(remote)}, fs: flakyFS{FS: remote, failures: &failures}}, nil
	}
	first, _ := redial()
	dials = 0
	var sleeps []time.Duration
	lc := newLiveConn(first, redial, 3, func(d time.Duration) { sleeps = append(sleeps, d) }, nil)

	if _, err := lc.FS().List(context.Background(), "/", true); err != nil {
		t.Fatalf("List should recover after 2 drops: %v", err)
	}
	if dials != 2 || len(sleeps) != 2 || sleeps[0] != time.Second || sleeps[1] != 2*time.Second {
		t.Fatalf("dials=%d sleeps=%v, want 2 redials with 1s,2s backoff", dials, sleeps)
	}

	// More failures than retries allow: the error surfaces.
	failures = 10
	sleeps = nil
	lc = newLiveConn(first, redial, 2, func(d time.Duration) { sleeps = append(sleeps, d) }, nil)
	if _, err := lc.FS().List(context.Background(), "/", true); err == nil {
		t.Fatalf("List should fail once retries are exhausted")
	}
	if len(sleeps) != 2 {
		t.Fatalf("slept %d times, want 2 (the retry budget)", len(sleeps))
	}

	// A real answer from the server is never retried.
	notFound := newLiveConn(first, redial, 5, func(time.Duration) { t.Fatal("must not back off for a permanent error") }, nil)
	if _, err := notFound.FS().Stat(context.Background(), "/missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat error = %v, want not-exist", err)
	}
}
