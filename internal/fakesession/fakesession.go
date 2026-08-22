// Package fakesession is the simulated session.Dialer used for UI work before
// real SFTP/FTP/FTPS clients exist. It connects to nothing: it hands back a
// fakefs tree and a faketransfer engine after a plausible delay.
package fakesession

import (
	"context"
	"fmt"
	"sync"
	"time"

	"tideftp/internal/fakefs"
	"tideftp/internal/faketransfer"
	"tideftp/internal/session"
	"tideftp/internal/transfer"
	"tideftp/internal/vfs"
)

var (
	_ session.Dialer = (*Dialer)(nil)
	_ session.Conn   = (*Conn)(nil)
)

// Dialer connects to the hosts it was told about and fails for anything else,
// so the UI's failure path is reachable by picking an unknown host.
type Dialer struct {
	known       map[string]bool
	dialLatency time.Duration
	listLatency time.Duration
}

// New builds a dialer that succeeds only for the named hosts. dialLatency
// stands in for connect time and listLatency for per-listing round trips;
// both are zero in tests.
func New(dialLatency, listLatency time.Duration, hosts ...string) *Dialer {
	known := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		known[host] = true
	}
	return &Dialer{known: known, dialLatency: dialLatency, listLatency: listLatency}
}

func (d *Dialer) Dial(ctx context.Context, target session.Target) (session.Conn, error) {
	if d.dialLatency > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(d.dialLatency):
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !d.known[target.Host] {
		return nil, fmt.Errorf("dial %s: no route to host", target.Address())
	}
	return &Conn{
		fs:     fakefs.NewRemoteWithLatency(d.listLatency),
		engine: faketransfer.New(),
		done:   make(chan error, 1),
	}, nil
}

// Conn is one simulated connection. Drop makes it fail on demand, which is how
// tests and manual runs exercise the UI's connection-lost path.
type Conn struct {
	fs     *fakefs.Remote
	engine *faketransfer.Engine
	done   chan error
	once   sync.Once
}

func (c *Conn) FS() vfs.FS              { return c.fs }
func (c *Conn) Engine() transfer.Engine { return c.engine }
func (c *Conn) Done() <-chan error      { return c.done }
func (c *Conn) Close() error            { c.end(nil); return nil }

// Drop ends the connection as if the server had gone away. err must not be nil;
// a nil reason is what Close means.
func (c *Conn) Drop(err error) {
	if err == nil {
		err = fmt.Errorf("connection reset by peer")
	}
	c.end(err)
}

// end is idempotent: whichever of Close or Drop happens first decides the
// reason, and the engine is shut down exactly once.
func (c *Conn) end(reason error) {
	c.once.Do(func() {
		_ = c.engine.Close()
		c.done <- reason
		close(c.done)
	})
}
