package cli

import (
	"context"
	"testing"

	"tideftp/internal/connect"
	"tideftp/internal/fakefs"
	"tideftp/internal/session"
)

type identityDialer struct {
	got  session.Credentials
	conn session.Conn
}

func (d *identityDialer) Dial(_ context.Context, _ session.Target, creds session.Credentials) (session.Conn, error) {
	d.got = creds
	return d.conn, nil
}

func TestProfileIdentityAndCLIOverride(t *testing.T) {
	for _, override := range []string{"", "/tmp/override_key"} {
		t.Run(override, func(t *testing.T) {
			remote := fakefs.NewRemote()
			app, _, _ := testApp(t, remote)
			spy := &identityDialer{conn: &fakeConn{fs: remote, eng: newCopyEngine(remote)}}
			app.Dial = func(connect.Options) (session.Dialer, error) { return spy, nil }
			target := session.Target{Protocol: session.ProtocolSFTP, Host: "test", IdentityFile: "/tmp/saved_key"}
			_, err := app.dial(target, session.Credentials{}, &connFlags{identity: override})
			if err != nil {
				t.Fatal(err)
			}
			want := target.IdentityFile
			if override != "" {
				want = override
			}
			if spy.got.IdentityFile != want {
				t.Fatalf("identity = %q, want %q", spy.got.IdentityFile, want)
			}
		})
	}
}
