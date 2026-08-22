package ftpsession

import (
	"context"
	"errors"
	"sync"

	"github.com/jlaffaye/ftp"
)

// An FTP control connection carries one command at a time: unlike an SSH
// connection, it cannot be shared between a directory listing and two running
// transfers. So a connection is a resource to be borrowed, and the pool caps
// how many exist at once — which also matters because a passive-mode server
// publishes a finite range of data ports.
type pool struct {
	dial func(context.Context) (*ftp.ServerConn, error)
	// sem bounds concurrent borrows. Taking a slot before looking for a
	// connection is what makes the cap hold.
	sem chan struct{}

	mu     sync.Mutex
	idle   []*ftp.ServerConn
	closed bool
}

var errPoolClosed = errors.New("connection closed")

func newPool(max int, dial func(context.Context) (*ftp.ServerConn, error)) *pool {
	if max < 1 {
		max = 1
	}
	return &pool{dial: dial, sem: make(chan struct{}, max)}
}

// seed adds an already-open connection, so the one dialled to prove the
// credentials work is not thrown away.
func (p *pool) seed(conn *ftp.ServerConn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = conn.Quit()
		return
	}
	p.idle = append(p.idle, conn)
}

// get borrows a connection, dialling a new one if the pool is under its cap
// and none are idle. Every successful get must be matched by put or discard.
func (p *pool) get(ctx context.Context) (*ftp.ServerConn, error) {
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		<-p.sem
		return nil, errPoolClosed
	}
	if n := len(p.idle); n > 0 {
		conn := p.idle[n-1]
		p.idle = p.idle[:n-1]
		p.mu.Unlock()
		return conn, nil
	}
	p.mu.Unlock()

	conn, err := p.dial(ctx)
	if err != nil {
		<-p.sem
		return nil, err
	}
	return conn, nil
}

// put returns a healthy connection for reuse.
func (p *pool) put(conn *ftp.ServerConn) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = conn.Quit()
		<-p.sem
		return
	}
	p.idle = append(p.idle, conn)
	p.mu.Unlock()
	<-p.sem
}

// discard drops a connection that errored. Reusing one whose control stream
// may be mid-response corrupts every later command on it.
func (p *pool) discard(conn *ftp.ServerConn) {
	if conn != nil {
		_ = conn.Quit()
	}
	<-p.sem
}

// Close shuts the idle connections. Borrowed ones are closed by whoever holds
// them, when they put or discard.
func (p *pool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	idle := p.idle
	p.idle = nil
	p.mu.Unlock()

	for _, conn := range idle {
		_ = conn.Quit()
	}
	return nil
}
