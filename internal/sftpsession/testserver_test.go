package sftpsession

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

// testServer is a real SSH server serving a real SFTP subsystem over a
// loopback listener, rooted at a temp directory. pkg/sftp ships the server
// half, so the adapter can be tested against genuine protocol traffic without
// an external sshd or a container.
type testServer struct {
	addr     string
	root     string
	hostKey  ssh.PublicKey
	clientPK string // path to the client's private key

	listener net.Listener
	wg       sync.WaitGroup

	mu      sync.Mutex
	closed  bool
	refused bool       // when set, connections are accepted then dropped
	conns   []net.Conn // accepted connections, closed on shutdown
}

// path is the absolute path of name on the server. pkg/sftp's server is not
// chrooted -- WithServerWorkingDirectory only sets the cwd for relative paths
// -- so tests address files by their real absolute path, exactly as a client
// talking to a real server would.
func (s *testServer) path(name string) string { return filepath.Join(s.root, name) }

func startTestServer(t *testing.T) *testServer {
	t.Helper()

	hostSigner, hostPub := newSigner(t)
	_, clientPub, clientPriv := newKeyPair(t)

	root := t.TempDir()
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	writeKey(t, keyPath, clientPriv)

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
	server := &testServer{
		addr:     listener.Addr().String(),
		root:     root,
		hostKey:  hostPub,
		clientPK: keyPath,
		listener: listener,
	}

	server.wg.Add(1)
	go func() {
		defer server.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			server.mu.Lock()
			refused := server.refused || server.closed
			if !refused {
				server.conns = append(server.conns, conn)
			}
			server.mu.Unlock()
			if refused {
				conn.Close()
				continue
			}
			server.wg.Add(1)
			go func() {
				defer server.wg.Done()
				server.serve(conn, config)
			}()
		}
	}()

	t.Cleanup(server.Close)
	return server
}

func (s *testServer) serve(conn net.Conn, config *ssh.ServerConfig) {
	defer conn.Close()
	sshConn, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(requests)

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
			server, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(s.root))
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

// refuse makes every later connection attempt fail, standing in for a server
// that has gone away.
func (s *testServer) refuse() {
	s.mu.Lock()
	s.refused = true
	s.mu.Unlock()
}

func (s *testServer) Close() {
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
	// Serving goroutines park until their client goes away, so closing the
	// listener alone would deadlock the wait below.
	for _, conn := range conns {
		_ = conn.Close()
	}
	s.wg.Wait()
}

// knownHostsFile writes a known_hosts naming this server's host key, so the
// adapter's real strict verification is what the tests exercise.
func (s *testServer) knownHostsFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{knownhosts.Normalize(s.addr)}, s.hostKey)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return path
}

// wrongKnownHostsFile names a different key for this address, which is what a
// man-in-the-middle would look like.
func (s *testServer) wrongKnownHostsFile(t *testing.T) string {
	t.Helper()
	_, otherPub := newSigner(t)
	path := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{knownhosts.Normalize(s.addr)}, otherPub)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return path
}

func (s *testServer) writeFile(t *testing.T, name string, body []byte) string {
	t.Helper()
	full := filepath.Join(s.root, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

// newSigner generates a fresh ed25519 key and returns it in the two forms the
// tests need: a signer for the SSH side and the public half for known_hosts.
func newSigner(t *testing.T) (ssh.Signer, ssh.PublicKey) {
	t.Helper()
	signer, pub, _ := newKeyPair(t)
	return signer, pub
}

func newKeyPair(t *testing.T) (ssh.Signer, ssh.PublicKey, ed25519.PrivateKey) {
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

// writeKey writes an OpenSSH-format private key, which is what the adapter's
// IdentityFiles path actually parses.
func writeKey(t *testing.T, path string, priv ed25519.PrivateKey) {
	t.Helper()
	block, err := ssh.MarshalPrivateKey(priv, "tideftp test key")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
}
