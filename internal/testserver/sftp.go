// Package testserver runs real, in-process FTP and SFTP servers on loopback so
// tests can drive TideFTP's adapters and CLI over genuine protocol traffic —
// no external daemon, no container. It is a regular (non-_test) package so
// every package's tests can import it, the way net/http/httptest is.
package testserver

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SFTP is an SSH server with an SFTP subsystem rooted at a temp directory.
// pkg/sftp's server is not chrooted, so files are addressed by their real
// absolute path (see Path), exactly as against a real server.
type SFTP struct {
	Addr string // host:port
	Root string

	hostKey  ssh.PublicKey
	keyPath  string
	listener net.Listener
	wg       sync.WaitGroup

	mu      sync.Mutex
	closed  bool
	refused bool
	conns   []net.Conn
}

// StartSFTP starts a server that accepts one generated client key.
func StartSFTP(t testing.TB) *SFTP {
	t.Helper()
	hostSigner, hostPub := newKeyPair(t)
	_, clientPub, clientPriv := newKeyPairWithPrivate(t)

	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	block, err := ssh.MarshalPrivateKey(clientPriv, "tideftp test key")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(clientPub.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("unknown public key")
		},
	}
	config.AddHostKey(hostSigner)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &SFTP{
		Addr:     listener.Addr().String(),
		Root:     t.TempDir(),
		hostKey:  hostPub,
		keyPath:  keyPath,
		listener: listener,
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			refused := s.refused || s.closed
			if !refused {
				s.conns = append(s.conns, conn)
			}
			s.mu.Unlock()
			if refused {
				_ = conn.Close()
				continue
			}
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.serve(conn, config)
			}()
		}
	}()
	t.Cleanup(s.Close)
	return s
}

func (s *SFTP) serve(conn net.Conn, config *ssh.ServerConfig) {
	defer conn.Close()
	sshConn, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go func() {
		for req := range requests {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}()
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		channel, reqs, err := newChannel.Accept()
		if err != nil {
			return
		}
		go func() {
			for req := range reqs {
				ok := req.Type == "subsystem" && len(req.Payload) >= 4 && string(req.Payload[4:]) == "sftp"
				if req.WantReply {
					_ = req.Reply(ok, nil)
				}
			}
		}()
		go func(channel ssh.Channel) {
			defer channel.Close()
			server, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(s.Root))
			if err != nil {
				return
			}
			defer server.Close()
			if err := server.Serve(); err != nil && err != io.EOF {
				return
			}
		}(channel)
	}
}

// Host and Port split Addr for command-line flags.
func (s *SFTP) Host() string { h, _, _ := net.SplitHostPort(s.Addr); return h }
func (s *SFTP) Port() string { _, p, _ := net.SplitHostPort(s.Addr); return p }

// Path is the absolute path of name on the server.
func (s *SFTP) Path(name string) string { return filepath.Join(s.Root, name) }

// IdentityFile is the client's private key, for --identity.
func (s *SFTP) IdentityFile() string { return s.keyPath }

// KnownHosts writes a known_hosts naming this server's key, for --known-hosts.
func (s *SFTP) KnownHosts(t testing.TB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{knownhosts.Normalize(s.Addr)}, s.hostKey)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return path
}

// WriteFile creates name (and its parents) under the server root.
func (s *SFTP) WriteFile(t testing.TB, name string, body []byte) {
	t.Helper()
	writeFile(t, s.Path(name), body)
}

// DropConnections severs every live connection but keeps listening, which is
// what a network blip looks like to a client: the next dial works.
func (s *SFTP) DropConnections() {
	s.mu.Lock()
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// Refuse makes every later connection fail, as a server that went away.
func (s *SFTP) Refuse() {
	s.mu.Lock()
	s.refused = true
	s.mu.Unlock()
}

// Close stops the server and drops every connection.
func (s *SFTP) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	_ = s.listener.Close()
	for _, c := range conns {
		_ = c.Close()
	}
	s.wg.Wait()
}

func newKeyPair(t testing.TB) (ssh.Signer, ssh.PublicKey) {
	signer, pub, _ := newKeyPairWithPrivate(t)
	return signer, pub
}

func newKeyPairWithPrivate(t testing.TB) (ssh.Signer, ssh.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return signer, signer.PublicKey(), priv
}

func writeFile(t testing.TB, full string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, body, 0o644); err != nil {
		t.Fatal(err)
	}
}
