package router

import (
	"context"
	"errors"
	"testing"

	"tideftp/internal/session"
)

// stubDialer records the target it was asked to dial and returns a fixed
// conn/err pair, so a test can tell which underlying Dialer actually ran.
type stubDialer struct {
	conn session.Conn
	err  error
	got  []session.Target
}

func (d *stubDialer) Dial(_ context.Context, target session.Target, _ session.Credentials) (session.Conn, error) {
	d.got = append(d.got, target)
	return d.conn, d.err
}

func TestDialRoutesByProtocol(t *testing.T) {
	sftp := &stubDialer{}
	ftp := &stubDialer{}
	ftps := &stubDialer{}
	d := New(map[string]session.Dialer{"sftp": sftp, "ftp": ftp, "ftps": ftps})

	if _, err := d.Dial(context.Background(), session.Target{Protocol: "ftp", Host: "a"}, session.Credentials{}); err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if len(ftp.got) != 1 || len(sftp.got) != 0 || len(ftps.got) != 0 {
		t.Fatalf("an ftp target reached ftp=%d sftp=%d ftps=%d dialers, want only ftp", len(ftp.got), len(sftp.got), len(ftps.got))
	}

	if _, err := d.Dial(context.Background(), session.Target{Protocol: "sftp", Host: "b"}, session.Credentials{}); err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if len(sftp.got) != 1 || sftp.got[0].Host != "b" {
		t.Fatalf("sftp dialer got %v, want one target for host b", sftp.got)
	}
}

func TestDialFailsForAnUnconfiguredProtocol(t *testing.T) {
	d := New(map[string]session.Dialer{"sftp": &stubDialer{}})

	_, err := d.Dial(context.Background(), session.Target{Protocol: "ftp", Host: "a"}, session.Credentials{})
	if err == nil {
		t.Fatalf("dialing a protocol with no configured Dialer returned no error")
	}
}

func TestDialPropagatesTheUnderlyingError(t *testing.T) {
	want := errors.New("connection refused")
	d := New(map[string]session.Dialer{"ftp": &stubDialer{err: want}})

	_, err := d.Dial(context.Background(), session.Target{Protocol: "ftp", Host: "a"}, session.Credentials{})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want the underlying dialer's error", err)
	}
}
