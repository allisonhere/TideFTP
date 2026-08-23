// Package sftpsession connects to a real server over SSH and implements all
// three adapter seams for it: session.Dialer, and — through the Conn it
// returns — vfs.FS for browsing and transfer.Engine for moving bytes.
//
// Authentication is the SSH agent, key files, or a password — typed into the
// connect form's Password field, or read from the environment when the form
// leaves it blank. A password is deliberately not a command-line flag: that
// would put it in the process table for every other user on the machine to
// read.
//
// Host keys are checked strictly against a known_hosts file. There is no
// option here to skip that check: "accept anything for now" is the kind of
// placeholder that survives to release, and the ask/accept-once flow belongs
// with the connect form that can actually ask.
package sftpsession

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
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
// PasswordEnv is the environment variable a password is read from when the
// server wants one.
const PasswordEnv = "TIDEFTP_SFTP_PASSWORD"

type Config struct {
	// KnownHostsPath is the file host keys are checked against. Empty means
	// ~/.ssh/known_hosts.
	KnownHostsPath string
	// Password authenticates when key-based methods do not. Empty means read
	// PasswordEnv; still empty means no password is offered at all.
	Password string
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

func (d *Dialer) Dial(ctx context.Context, target session.Target, creds session.Credentials) (session.Conn, error) {
	if target.User == "" {
		return nil, errors.New("no username configured for this profile")
	}
	auth, err := d.authMethods(creds)
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

	clientConfig := &ssh.ClientConfig{
		User:            target.User,
		Auth:            auth,
		HostKeyCallback: hostKeys,
		Timeout:         timeout,
	}
	// Restrict negotiation to the host key types actually pinned for this
	// host, or the server offers whichever type the client prefers and a
	// known host fails as a "key mismatch".
	if algorithms := pinnedHostKeyAlgorithms(hostKeys, address); len(algorithms) > 0 {
		clientConfig.HostKeyAlgorithms = algorithms
	}

	clientConn, channels, requests, err := ssh.NewClientConn(conn, address, clientConfig)
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

// authMethods builds the auth methods to offer, in the order they are tried.
// creds.PasswordOnly bypasses the agent and key files entirely rather than
// merely appending Password after them — it is what "password" means as an
// explicit choice in the connect form, not just another fallback.
func (d *Dialer) authMethods(creds session.Credentials) ([]ssh.AuthMethod, error) {
	if creds.PasswordOnly {
		if creds.Password == "" {
			return nil, errors.New("password auth was chosen but no password was given")
		}
		return []ssh.AuthMethod{ssh.Password(creds.Password)}, nil
	}
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
	// Password goes last so the key-based methods are tried first.
	if password := d.password(creds.Password); password != "" {
		methods = append(methods, ssh.Password(password))
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("no usable credentials: start an ssh agent, configure a key file, or set %s", PasswordEnv)
	}
	return methods, nil
}

// password resolves the password to offer: an explicit override from this
// Dial's Credentials takes priority, then the Dialer's own Config, then the
// environment.
func (d *Dialer) password(override string) string {
	if override != "" {
		return override
	}
	if d.cfg.Password != "" {
		return d.cfg.Password
	}
	return os.Getenv(PasswordEnv)
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

// pinnedHostKeyAlgorithms reports which host key types known_hosts holds for
// address.
//
// A host with several host keys — most do — offers whichever type the client
// asks for. If known_hosts pins only the ed25519 key and the client negotiates
// RSA, verification fails with "key mismatch" even though the host is known
// and unchanged. x/crypto has no helper for this, so the trick is to ask the
// callback about a key that cannot possibly match: for a known host it returns
// a KeyError listing the keys it does have.
func pinnedHostKeyAlgorithms(callback ssh.HostKeyCallback, address string) []string {
	_, probe, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil
	}
	signer, err := ssh.NewSignerFromKey(probe)
	if err != nil {
		return nil
	}
	remote, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		return nil
	}

	var keyErr *knownhosts.KeyError
	if !errors.As(callback(address, remote, signer.PublicKey()), &keyErr) {
		return nil
	}

	seen := map[string]bool{}
	algorithms := make([]string, 0, len(keyErr.Want)*3)
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			algorithms = append(algorithms, name)
		}
	}
	for _, known := range keyErr.Want {
		kind := known.Key.Type()
		add(kind)
		if kind == ssh.KeyAlgoRSA {
			// The same RSA host key also serves the SHA-2 signature
			// algorithms, which modern servers prefer and some require.
			add(ssh.KeyAlgoRSASHA256)
			add(ssh.KeyAlgoRSASHA512)
		}
	}
	return algorithms
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
