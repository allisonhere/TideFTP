package ftpsession

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	ftpserver "goftp.io/server/v2"
	filedriver "goftp.io/server/v2/driver/file"

	"tideftp/internal/session"
)

const (
	ftpTestUser = "tester"
	ftpTestPass = "s3cret"
)

// ftpTestServer is an in-process FTP server backed by a temp directory. It
// lets the adapter's protocol-level behaviour — listing, mkdir, rename,
// remove, and the transfer engine moving real bytes — be exercised without a
// live vsftpd, the way internal/sftpsession has had an in-process SSH server
// all along.
type ftpTestServer struct {
	root string
	addr string
}

func startFTPServer(t *testing.T) *ftpTestServer {
	t.Helper()
	root := t.TempDir()

	driver, err := filedriver.NewDriver(root)
	if err != nil {
		t.Fatalf("ftp file driver: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv, err := ftpserver.NewServer(&ftpserver.Options{
		Driver:   driver,
		Auth:     &ftpserver.SimpleAuth{Name: ftpTestUser, Password: ftpTestPass},
		Perm:     ftpserver.NewSimplePerm("tester", "tester"),
		Hostname: "127.0.0.1",
		Logger:   new(ftpserver.DiscardLogger),
	})
	if err != nil {
		_ = listener.Close()
		t.Fatalf("ftp server: %v", err)
	}

	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Shutdown() })

	return &ftpTestServer{root: root, addr: listener.Addr().String()}
}

// path resolves a server-relative path to its location on disk.
func (s *ftpTestServer) path(name string) string { return filepath.Join(s.root, name) }

func (s *ftpTestServer) writeFile(t *testing.T, name string, body []byte) {
	t.Helper()
	full := s.path(name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (s *ftpTestServer) connect(t *testing.T) session.Conn {
	t.Helper()
	host, port, err := net.SplitHostPort(s.addr)
	if err != nil {
		t.Fatalf("split %q: %v", s.addr, err)
	}
	p, _ := strconv.Atoi(port)
	target := session.Target{Protocol: "ftp", Host: host, Port: p, User: ftpTestUser, StartPath: "/"}

	conn, err := New(Config{Timeout: 10 * time.Second}).
		Dial(context.Background(), target, session.Credentials{Password: ftpTestPass})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}
