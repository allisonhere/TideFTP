package ftpsession

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"tideftp/internal/session"
	"tideftp/internal/testserver"
)

// dialAs connects to srv as user/pass, returning Dial's error.
func dialAs(t *testing.T, srv *testserver.FTP, user, pass string) error {
	t.Helper()
	host, port, _ := net.SplitHostPort(srv.Addr)
	p, _ := strconv.Atoi(port)
	target := session.Target{Protocol: "ftp", Host: host, Port: p, User: user, StartPath: "/"}
	conn, err := New(Config{Timeout: 5 * time.Second}).Dial(context.Background(), target, session.Credentials{Password: pass})
	if err == nil {
		_ = conn.Close()
	}
	return err
}

// Issue #3: a user reported TideFTP "appends @domain.com to the username". It
// does not — the name is sent as typed — so pin that down: the server must see
// exactly the string entered, whether or not it resembles the host or already
// contains an @.
func TestLoginSendsTheUsernameVerbatim(t *testing.T) {
	for _, user := range []string{"jan", "jan@other.org", "JAN", "ftp_user-1"} {
		srv := testserver.StartFTPAs(t, user, "pw")
		if err := dialAs(t, srv, user, "pw"); err != nil {
			t.Fatalf("login as %q: %v", user, err)
		}
		got := srv.LoginNames()
		if len(got) == 0 || got[0] != user {
			t.Fatalf("server saw login names %q, want exactly %q (nothing appended)", got, user)
		}
	}
}

// A failed login must not read as though a domain was appended to the name.
func TestFailedLoginErrorQuotesTheUsername(t *testing.T) {
	srv := testserver.StartFTPAs(t, "jan", "right")
	err := dialAs(t, srv, "jan", "wrong")
	if err == nil {
		t.Fatalf("a wrong password should fail")
	}
	msg := err.Error()
	host, _, _ := net.SplitHostPort(srv.Addr)
	if !strings.Contains(msg, `login as "jan" on `) {
		t.Fatalf("error %q should say which username was sent", msg)
	}
	if strings.Contains(msg, "jan@"+host) || strings.Contains(msg, "jan@127") {
		t.Fatalf("error %q reads as if @host was appended to the login", msg)
	}
	if got := srv.LoginNames(); len(got) == 0 || got[0] != "jan" {
		t.Fatalf("server saw %q, want jan", got)
	}
}
