package testserver

import (
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	ftpserver "goftp.io/server/v2"
	filedriver "goftp.io/server/v2/driver/file"
)

// FTP credentials every FTP server accepts.
const (
	FTPUser = "tester"
	FTPPass = "s3cret"
)

// FTP is a plain-FTP server rooted at a temp directory. Unlike SFTP its root
// is the protocol's "/", so remote paths are slash-rooted relative to Root.
type FTP struct {
	Addr string
	Root string

	listener *trackingListener
	auth     *recordingAuth
}

// StartFTP starts the server and registers its shutdown with t.
func StartFTP(t testing.TB) *FTP { return StartFTPAs(t, FTPUser, FTPPass) }

// recordingAuth accepts one account and remembers every username it is asked
// to check, exactly as the client sent it.
type recordingAuth struct {
	inner *ftpserver.SimpleAuth
	mu    sync.Mutex
	names []string
}

func (a *recordingAuth) CheckPasswd(ctx *ftpserver.Context, name, pass string) (bool, error) {
	a.mu.Lock()
	a.names = append(a.names, name)
	a.mu.Unlock()
	return a.inner.CheckPasswd(ctx, name, pass)
}

// LoginNames lists the usernames clients have presented, in order.
func (s *FTP) LoginNames() []string {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	return append([]string(nil), s.auth.names...)
}

// StartFTPAs is StartFTP for an account other than the default one.
func StartFTPAs(t testing.TB, user, pass string) *FTP {
	t.Helper()
	root := t.TempDir()
	driver, err := filedriver.NewDriver(root)
	if err != nil {
		t.Fatalf("ftp file driver: %v", err)
	}
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	listener := &trackingListener{Listener: raw}
	auth := &recordingAuth{inner: &ftpserver.SimpleAuth{Name: user, Password: pass}}
	srv, err := ftpserver.NewServer(&ftpserver.Options{
		Driver:   driver,
		Auth:     auth,
		Perm:     ftpserver.NewSimplePerm("tester", "tester"),
		Hostname: "127.0.0.1",
		Logger:   new(ftpserver.DiscardLogger),
	})
	if err != nil {
		_ = raw.Close()
		t.Fatalf("ftp server: %v", err)
	}
	go func() { _ = srv.Serve(listener) }()
	waitForBanner(t, raw.Addr().String())
	t.Cleanup(func() { _ = srv.Shutdown() })
	return &FTP{Addr: raw.Addr().String(), Root: root, listener: listener, auth: auth}
}

// waitForBanner returns once the server has accepted a connection and greeted
// it. goftp's Serve initialises its fields as it starts, so shutting it down
// before it has done so is a data race; one round trip is enough to be past it.
func waitForBanner(t testing.TB, addr string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("ftp server did not start: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("ftp server sent no banner: %v", err)
	}
}

func (s *FTP) Host() string { h, _, _ := net.SplitHostPort(s.Addr); return h }
func (s *FTP) Port() string { _, p, _ := net.SplitHostPort(s.Addr); return p }

// Path resolves a server-relative path to its place on disk.
func (s *FTP) Path(name string) string { return filepath.Join(s.Root, name) }

// WriteFile creates name (and its parents) under the server root.
func (s *FTP) WriteFile(t testing.TB, name string, body []byte) {
	t.Helper()
	writeFile(t, s.Path(name), body)
}

// DropConnections closes every accepted control connection while the server
// keeps listening — a network blip.
func (s *FTP) DropConnections() { s.listener.dropAll() }

type trackingListener struct {
	net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func (l *trackingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.mu.Lock()
		l.conns = append(l.conns, c)
		l.mu.Unlock()
	}
	return c, err
}

func (l *trackingListener) dropAll() {
	l.mu.Lock()
	conns := l.conns
	l.conns = nil
	l.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}
