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
	"crypto/x509"
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
	// DefaultMaxTLSVersion caps FTPS at TLS 1.2.
	//
	// This is deliberate and measured, not caution. Against vsftpd, uploads
	// over a TLS 1.3 data connection fail at exactly 16384 bytes and above
	// with 426 "Failure reading network stream", while anything smaller
	// succeeds; the same uploads at any size succeed over TLS 1.2. The
	// server is not at fault in general — curl uploads happily over TLS 1.3 —
	// so this is an interop problem between crypto/tls and vsftpd's data
	// connection handling that cannot be fixed from the client side.
	//
	// FTPS servers skew old, so a cap that works everywhere beats a default
	// that silently corrupts large uploads. Config.MaxTLSVersion raises it.
	DefaultMaxTLSVersion = tls.VersionTLS13 - 1
	// sessionCacheSize only ever holds one session per server, but the cache
	// is per-connection and a couple of spare slots cost nothing.
	sessionCacheSize = 8
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
	// ExplicitTLS upgrades the connection with AUTH TLS (FTPS). Note that not
	// every server advertises AUTH in FEAT even when it supports it — vsftpd
	// does not — so this is a setting rather than something to autodetect.
	ExplicitTLS bool
	// TLSConfig overrides everything below when set.
	TLSConfig *tls.Config
	// RootCAFile is a PEM bundle trusted in addition to the system roots.
	// Pointing it at a self-signed server certificate is how to use one
	// without turning verification off, which is why it exists.
	RootCAFile string
	// InsecureSkipVerify accepts any certificate. It is off by default and
	// belongs nowhere but a lab; prefer RootCAFile.
	InsecureSkipVerify bool
	// MaxTLSVersion caps the negotiated version. Zero means
	// DefaultMaxTLSVersion; see the comment there for why that is not the
	// newest version crypto/tls supports.
	MaxTLSVersion uint16
	Timeout       time.Duration
	MaxConns      int
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
			config, err := d.tlsConfig(target)
			if err != nil {
				return nil, err
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

// tlsConfig builds the FTPS client configuration. Verification is on unless
// explicitly disabled: a self-signed server certificate is meant to be handled
// by trusting it through RootCAFile, not by accepting every certificate.
func (d *Dialer) tlsConfig(target session.Target) (*tls.Config, error) {
	if d.cfg.TLSConfig != nil {
		return d.cfg.TLSConfig.Clone(), nil
	}
	maxVersion := d.cfg.MaxTLSVersion
	if maxVersion == 0 {
		maxVersion = DefaultMaxTLSVersion
	}
	config := &tls.Config{
		ServerName:         target.Host,
		MinVersion:         tls.VersionTLS12,
		MaxVersion:         maxVersion,
		InsecureSkipVerify: d.cfg.InsecureSkipVerify,
		// vsftpd defaults to require_ssl_reuse=YES: the data connection must
		// resume the control connection's TLS session, or it answers "522 SSL
		// connection failed: session reuse required". jlaffaye/ftp has no
		// option for this, but it hands the same config to both connections,
		// so a session cache lets crypto/tls resume on its own.
		ClientSessionCache: tls.NewLRUClientSessionCache(sessionCacheSize),
	}
	if d.cfg.RootCAFile == "" {
		return config, nil
	}

	pemBytes, err := os.ReadFile(d.cfg.RootCAFile)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", d.cfg.RootCAFile, err)
	}
	// Start from the system roots so pinning one server does not stop every
	// normally-trusted server from working.
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("%s contains no certificates", d.cfg.RootCAFile)
	}
	config.RootCAs = roots
	return config, nil
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
