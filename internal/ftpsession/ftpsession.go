// Package ftpsession connects to an FTP or FTPS server and implements all
// three adapter seams for it: session.Dialer, and through the Conn it returns
// vfs.FS for browsing and transfer.Engine for moving bytes.
//
// The shape differs from internal/sftpsession in one way that drives the whole
// package: an FTP control connection carries a single command at a time, so a
// listing and two running transfers cannot share one. Connections are pooled
// and borrowed instead. See pool.go.
package ftpsession

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jlaffaye/ftp"

	"tideftp/internal/session"
	"tideftp/internal/transfer"
	"tideftp/internal/vfs"
)

var (
	_ session.Dialer = (*Dialer)(nil)
	_ session.Conn   = (*Conn)(nil)
)

const (
	DefaultTimeout = 30 * time.Second
	// DefaultMaxConns caps concurrent control connections. It has to cover the
	// transfer parallelism plus browsing, and stay inside the data-port range
	// a passive-mode server publishes.
	DefaultMaxConns = 4
	// keepaliveInterval is how often an idle connection is pinged. FTP has no
	// equivalent of ssh.Client.Wait, so a dropped connection is only noticed by
	// trying to use one.
	keepaliveInterval = 30 * time.Second
)

// PasswordEnv is the environment variable the password is read from. FTP
// authenticates with one and the app has no prompt yet; a command-line flag
// would put it in the process table for every other user to read.
const PasswordEnv = "TIDEFTP_FTP_PASSWORD"

type Config struct {
	// Password authenticates the user. Empty means read PasswordEnv.
	Password string
	// ExplicitTLS upgrades the connection with AUTH TLS (FTPS). Servers that
	// do not advertise it will refuse.
	ExplicitTLS bool
	// TLSConfig is used when ExplicitTLS is set. Nil means a default config
	// that verifies the server certificate.
	TLSConfig *tls.Config
	Timeout   time.Duration
	MaxConns  int
}

type Dialer struct {
	cfg Config
}

func New(cfg Config) *Dialer { return &Dialer{cfg: cfg} }

func (d *Dialer) Dial(ctx context.Context, target session.Target) (session.Conn, error) {
	if target.User == "" {
		return nil, errors.New("no username configured for this profile")
	}
	password := d.cfg.Password
	if password == "" {
		password = os.Getenv(PasswordEnv)
	}
	if password == "" {
		return nil, fmt.Errorf("no password: set %s", PasswordEnv)
	}

	timeout := d.cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	address := target.Address()

	dial := func(ctx context.Context) (*ftp.ServerConn, error) {
		options := []ftp.DialOption{
			ftp.DialWithContext(ctx),
			ftp.DialWithTimeout(timeout),
		}
		if d.cfg.ExplicitTLS {
			config := d.cfg.TLSConfig
			if config == nil {
				config = &tls.Config{ServerName: target.Host, MinVersion: tls.VersionTLS12}
			}
			options = append(options, ftp.DialWithExplicitTLS(config))
		}
		conn, err := ftp.Dial(address, options...)
		if err != nil {
			return nil, fmt.Errorf("dial %s: %w", address, err)
		}
		if err := conn.Login(target.User, password); err != nil {
			_ = conn.Quit()
			return nil, fmt.Errorf("login %s@%s: %w", target.User, address, err)
		}
		return conn, nil
	}

	// Dial once up front so bad credentials or an unreachable host fail here,
	// where the UI can report them, rather than on the first listing.
	first, err := dial(ctx)
	if err != nil {
		return nil, err
	}

	maxConns := d.cfg.MaxConns
	if maxConns < 1 {
		maxConns = DefaultMaxConns
	}
	connections := newPool(maxConns, dial)
	connections.seed(first)

	return newConn(connections), nil
}

// Conn is one logical FTP connection: a pool of control connections plus the
// filesystem and engine that borrow from it.
type Conn struct {
	pool   *pool
	fs     *FS
	engine *Engine
	done   chan error
	stop   chan struct{}
	once   sync.Once
}

func newConn(connections *pool) *Conn {
	conn := &Conn{
		pool:   connections,
		fs:     &FS{pool: connections},
		engine: newEngine(connections),
		done:   make(chan error, 1),
		stop:   make(chan struct{}),
	}
	go conn.keepalive()
	return conn
}

func (c *Conn) FS() vfs.FS              { return c.fs }
func (c *Conn) Engine() transfer.Engine { return c.engine }
func (c *Conn) Done() <-chan error      { return c.done }

func (c *Conn) Close() error {
	c.end(nil)
	_ = c.engine.Close()
	return c.pool.Close()
}

// keepalive notices a server that has gone away. FTP offers no notification,
// so the only way to find out is to send something and see what happens.
func (c *Conn) keepalive() {
	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
			if err := c.ping(); err != nil {
				c.end(err)
				return
			}
		}
	}
}

// ping borrows a connection and sends NOOP. If none can be borrowed quickly
// the pool is busy, which is itself evidence the connection is alive, so the
// tick is skipped rather than reported as a failure.
func (c *Conn) ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := c.pool.get(ctx)
	if err != nil {
		if errors.Is(err, errPoolClosed) {
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if err := conn.NoOp(); err != nil {
		c.pool.discard(conn)
		return err
	}
	c.pool.put(conn)
	return nil
}

func (c *Conn) end(reason error) {
	c.once.Do(func() {
		close(c.stop)
		c.done <- reason
		close(c.done)
	})
}
