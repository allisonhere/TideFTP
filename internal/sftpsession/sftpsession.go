// Package sftpsession connects to a real server over SSH and implements all
// three adapter seams for it: session.Dialer, and — through the Conn it
// returns — vfs.FS for browsing and transfer.Engine for moving bytes.
//
// Authentication is limited to the SSH agent and key files on purpose. Both
// work without typing a secret, and the app has no text input yet; password
// auth arrives with the connect form.
//
// Host keys are checked strictly against a known_hosts file. There is no
// option here to skip that check: "accept anything for now" is the kind of
// placeholder that survives to release, and the ask/accept-once flow belongs
// with the connect form that can actually ask.
package sftpsession

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"tideftp/internal/session"
	"tideftp/internal/transfer"
	"tideftp/internal/vfs"
)

var (
	_ session.Dialer = (*Dialer)(nil)
	_ session.Conn   = (*Conn)(nil)
)

// Config is how a Dialer authenticates and verifies hosts.
type Config struct {
	// KnownHostsPath is the file host keys are checked against. Empty means
	// ~/.ssh/known_hosts.
	KnownHostsPath string
	// IdentityFiles are private key paths to offer, in order. Encrypted keys
	// are reported rather than prompted for.
	IdentityFiles []string
	// UseAgent offers the keys held by the agent at SSH_AUTH_SOCK.
	UseAgent bool
	// Timeout bounds the TCP connect and SSH handshake. Zero means
	// DefaultTimeout.
	Timeout time.Duration
}

const DefaultTimeout = 30 * time.Second

type Dialer struct {
	cfg Config
}

func New(cfg Config) *Dialer { return &Dialer{cfg: cfg} }

// DefaultConfig is agent auth plus the usual key names, verified against the
// user's known_hosts.
func DefaultConfig() Config {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{UseAgent: true}
	}
	ssh := filepath.Join(home, ".ssh")
	return Config{
		KnownHostsPath: filepath.Join(ssh, "known_hosts"),
		IdentityFiles: []string{
			filepath.Join(ssh, "id_ed25519"),
			filepath.Join(ssh, "id_rsa"),
		},
		UseAgent: true,
	}
}

func (d *Dialer) Dial(ctx context.Context, target session.Target) (session.Conn, error) {
	if target.User == "" {
		return nil, errors.New("no username configured for this profile")
	}
	auth, err := d.authMethods()
	if err != nil {
		return nil, err
	}
	hostKeys, err := d.hostKeyCallback()
	if err != nil {
		return nil, err
	}

	timeout := d.cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	address := target.Address()

	netDialer := &net.Dialer{Timeout: timeout}
	conn, err := netDialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", address, err)
	}

	// The handshake has no context of its own, so a deadline stands in for one.
	// It is cleared once the connection is up, or every later read would
	// inherit it.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}

	clientConn, channels, requests, err := ssh.NewClientConn(conn, address, &ssh.ClientConfig{
		User:            target.User,
		Auth:            auth,
		HostKeyCallback: hostKeys,
		Timeout:         timeout,
	})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh %s: %w", address, err)
	}
	_ = conn.SetDeadline(time.Time{})

	sshClient := ssh.NewClient(clientConn, channels, requests)
	client, err := sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		return nil, fmt.Errorf("sftp %s: %w", address, err)
	}
	return newConn(sshClient, client), nil
}

func (d *Dialer) authMethods() ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if d.cfg.UseAgent {
		if method, err := agentAuth(); err == nil {
			methods = append(methods, method)
		}
	}
	for _, path := range d.cfg.IdentityFiles {
		raw, err := os.ReadFile(path)
		if err != nil {
			// A key that is not there is not an error; a profile can name
			// several and use whichever exists.
			continue
		}
		signer, err := ssh.ParsePrivateKey(raw)
		if err != nil {
			var passphraseErr *ssh.PassphraseMissingError
			if errors.As(err, &passphraseErr) {
				return nil, fmt.Errorf("%s is passphrase-protected; use an ssh agent until password entry exists", path)
			}
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if len(methods) == 0 {
		return nil, errors.New("no usable credentials: start an ssh agent or configure a key file")
	}
	return methods, nil
}

func agentAuth() (ssh.AuthMethod, error) {
	socket := os.Getenv("SSH_AUTH_SOCK")
	if socket == "" {
		return nil, errors.New("no ssh agent (SSH_AUTH_SOCK is unset)")
	}
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil, fmt.Errorf("ssh agent: %w", err)
	}
	return ssh.PublicKeysCallback(agent.NewClient(conn).Signers), nil
}

func (d *Dialer) hostKeyCallback() (ssh.HostKeyCallback, error) {
	path := d.cfg.KnownHostsPath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("locate known_hosts: %w", err)
		}
		path = filepath.Join(home, ".ssh", "known_hosts")
	}
	callback, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return callback, nil
}

// Conn is one live SSH connection, shared by the filesystem and the engine.
type Conn struct {
	ssh    *ssh.Client
	client *sftp.Client
	fs     *FS
	engine *Engine
	done   chan error
	once   sync.Once
}

func newConn(sshClient *ssh.Client, client *sftp.Client) *Conn {
	conn := &Conn{
		ssh:    sshClient,
		client: client,
		fs:     &FS{client: client},
		done:   make(chan error, 1),
	}
	conn.engine = newEngine(client)

	// The connection ending on its own is a drop. Close reports first, so a
	// Wait that returns because we closed does not masquerade as one.
	go func() {
		err := sshClient.Wait()
		if err == nil {
			err = errors.New("connection closed by server")
		}
		conn.end(err)
	}()
	return conn
}

func (c *Conn) FS() vfs.FS              { return c.fs }
func (c *Conn) Engine() transfer.Engine { return c.engine }
func (c *Conn) Done() <-chan error      { return c.done }

func (c *Conn) Close() error {
	c.end(nil)
	_ = c.engine.Close()
	_ = c.client.Close()
	return c.ssh.Close()
}

func (c *Conn) end(reason error) {
	c.once.Do(func() {
		c.done <- reason
		close(c.done)
	})
}
