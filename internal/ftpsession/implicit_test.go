package ftpsession

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"tideftp/internal/session"
)

func implicitTarget(t *testing.T, addr string) session.Target {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	p, _ := strconv.Atoi(port)
	return session.Target{
		Protocol:  session.ProtocolFTPSImplicit,
		Host:      host,
		Port:      p,
		User:      ftpTestUser,
		StartPath: "/",
	}
}

// TestImplicitTLSDialsAndLists is the end-to-end proof: a dialer configured
// for implicit TLS handshakes before sending anything, logs in, and browses a
// server that answers nothing in the clear.
func TestImplicitTLSDialsAndLists(t *testing.T) {
	server := startImplicitServer(t)

	dialer := New(Config{ImplicitTLS: true, RootCAFile: server.caFile, Timeout: 10 * time.Second})
	conn, err := dialer.Dial(context.Background(), implicitTarget(t, server.addr), session.Credentials{Password: ftpTestPass})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	entries, err := conn.FS().List(ftpCtx(t), "/", false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := names(entries)
	for _, want := range []string{"notes.txt", "reports"} {
		if !contains(got, want) {
			t.Fatalf("List = %v, want it to include %q", got, want)
		}
	}
	if server.sawPlaintext() {
		t.Error("client sent FTP in the clear before the TLS handshake")
	}
}

// TestExplicitTLSCannotDialImplicitServer is why the two are separate
// protocols rather than one with a fallback. An AUTH TLS dialer opens the
// connection in the clear and waits for a greeting the server will not send
// until it has a ClientHello, so there is nothing to negotiate and nothing to
// auto-detect — which is also why the explicit flavour must not default to
// port 990.
func TestExplicitTLSCannotDialImplicitServer(t *testing.T) {
	server := startImplicitServer(t)

	dialer := New(Config{ExplicitTLS: true, RootCAFile: server.caFile, Timeout: 2 * time.Second})
	target := implicitTarget(t, server.addr)
	target.Protocol = session.ProtocolFTPS

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	started := time.Now()
	conn, err := dialer.Dial(ctx, target, session.Credentials{Password: ftpTestPass})
	if err == nil {
		conn.Close()
		t.Fatal("explicit-TLS dial of an implicit server succeeded, want failure")
	}
	// It has to fail *promptly*. The server accepts the connection and then
	// says nothing, and jlaffaye/ftp reads the greeting with no deadline of
	// its own, so without dialControl's the dial hangs until something else
	// gives up — a spinner the user can only escape by killing the app.
	if elapsed := time.Since(started); elapsed > 3*dialer.cfg.Timeout {
		t.Errorf("dial took %s to fail with a %s timeout; the greeting read is unbounded", elapsed, dialer.cfg.Timeout)
	}
}

// TestImplicitTLSVerifiesCertificate keeps the self-signed certificate from
// being trusted by accident: without the CA file the dial must fail, so
// RootCAFile is doing real work above rather than verification being off.
func TestImplicitTLSVerifiesCertificate(t *testing.T) {
	server := startImplicitServer(t)

	dialer := New(Config{ImplicitTLS: true, Timeout: 5 * time.Second})
	conn, err := dialer.Dial(context.Background(), implicitTarget(t, server.addr), session.Credentials{Password: ftpTestPass})
	if err == nil {
		conn.Close()
		t.Fatal("dial trusted an untrusted self-signed certificate, want failure")
	}
}

// TestImplicitTLSInsecureSkipsVerification is the escape hatch's counterpart:
// the same untrusted server dials fine once the caller asks for it explicitly,
// so FTPSInsecure reaches the implicit path and not only the explicit one.
func TestImplicitTLSInsecureSkipsVerification(t *testing.T) {
	server := startImplicitServer(t)

	dialer := New(Config{ImplicitTLS: true, Timeout: 10 * time.Second})
	creds := session.Credentials{Password: ftpTestPass, FTPSInsecure: true}
	conn, err := dialer.Dial(context.Background(), implicitTarget(t, server.addr), creds)
	if err != nil {
		t.Fatalf("Dial with FTPSInsecure: %v", err)
	}
	conn.Close()
}

// TestImplicitTLSRejectsBadPassword pins that authentication still happens
// after the handshake rather than the connection being considered good the
// moment TLS is up.
func TestImplicitTLSRejectsBadPassword(t *testing.T) {
	server := startImplicitServer(t)

	dialer := New(Config{ImplicitTLS: true, RootCAFile: server.caFile, Timeout: 10 * time.Second})
	conn, err := dialer.Dial(context.Background(), implicitTarget(t, server.addr), session.Credentials{Password: "wrong"})
	if err == nil {
		conn.Close()
		t.Fatal("Dial with a bad password succeeded, want failure")
	}
}

// TestBothTLSModesRejected catches the misconfiguration at Dial rather than
// letting the order options are applied in silently decide which mode is used.
func TestBothTLSModesRejected(t *testing.T) {
	dialer := New(Config{ExplicitTLS: true, ImplicitTLS: true})
	target := session.Target{Protocol: session.ProtocolFTPS, Host: "127.0.0.1", Port: 21, User: "tester"}
	if _, err := dialer.Dial(context.Background(), target, session.Credentials{Password: "x"}); err == nil {
		t.Fatal("Dial accepted both TLS modes, want an error")
	}
}
