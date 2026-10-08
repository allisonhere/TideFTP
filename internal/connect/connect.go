// Package connect builds protocol dialers and resolves connection targets for
// both the interactive app and the non-interactive CLI, so the two cannot
// drift apart in how they reach a server.
//
// It is deliberately free of any UI framework: cmd/tideftp wires it into the
// Bubble Tea model, and internal/cli calls it directly.
package connect

import (
	"crypto/tls"
	"fmt"
	"os/user"
	"time"

	"tideftp/internal/config"
	"tideftp/internal/fakesession"
	"tideftp/internal/ftpsession"
	"tideftp/internal/router"
	"tideftp/internal/session"
	"tideftp/internal/sftpsession"
)

// Options describes how to reach a server. Every field maps to a command-line
// flag or a saved profile; credentials are deliberately absent, because a
// password is never a flag and is resolved per attempt.
type Options struct {
	Demo           bool
	Protocol       string
	Host           string
	Port           int
	Username       string
	StartPath      string
	Identity       string
	KnownHosts     string
	FTPSCA         string
	FTPSInsecure   bool
	FTPSAllowTLS13 bool
}

// Dialer builds a router over every protocol adapter, unless Demo asks for the
// simulated one. Every protocol is always dialable — not just the one named —
// because the interactive connect form lets the user pick any of
// sftp/ftp/ftps/ftps-implicit per attempt.
func Dialer(o Options) (session.Dialer, error) {
	if o.Demo {
		return demoSession(), nil
	}

	switch o.Protocol {
	case session.ProtocolSFTP, session.ProtocolFTP, session.ProtocolFTPS, session.ProtocolFTPSImplicit:
	default:
		return nil, fmt.Errorf("unknown protocol %q: want sftp, ftp, ftps, or ftps-implicit", o.Protocol)
	}

	// RootCAFile/InsecureSkipVerify/MaxTLSVersion are TLS-only settings, so
	// plain FTP — no TLS at all — leaves ftpConfig at its zero value. Implicit
	// FTPS shares every TLS setting with explicit FTPS, including the TLS 1.2
	// cap, whose data-connection interop problem is not about how the control
	// connection was secured.
	ftpConfig := ftpsession.Config{}
	ftpsConfig := ftpsession.Config{
		ExplicitTLS:        true,
		RootCAFile:         o.FTPSCA,
		InsecureSkipVerify: o.FTPSInsecure,
	}
	ftpsImplicitConfig := ftpsConfig
	ftpsImplicitConfig.ExplicitTLS = false
	ftpsImplicitConfig.ImplicitTLS = true
	if o.FTPSAllowTLS13 {
		ftpsConfig.MaxTLSVersion = tls.VersionTLS13
		ftpsImplicitConfig.MaxTLSVersion = tls.VersionTLS13
	}

	sshConfig := sftpsession.DefaultConfig()
	if o.Identity != "" {
		sshConfig.IdentityFiles = []string{o.Identity}
		sshConfig.UseAgent = false
	}
	if o.KnownHosts != "" {
		sshConfig.KnownHostsPath = o.KnownHosts
	}

	return router.New(map[string]session.Dialer{
		session.ProtocolSFTP:         sftpsession.New(sshConfig),
		session.ProtocolFTP:          ftpsession.New(ftpConfig),
		session.ProtocolFTPS:         ftpsession.New(ftpsConfig),
		session.ProtocolFTPSImplicit: ftpsession.New(ftpsImplicitConfig),
	}), nil
}

// Session is what the interactive app needs: a dialer plus the initial targets
// to auto-connect to. Without a host there are no targets, and the app just
// opens ready for the connect form. The demo dialer comes with a fixed set.
func Session(o Options) (session.Dialer, []session.Target, error) {
	if o.Demo {
		return demoSession(), demoTargets(), nil
	}
	dialer, err := Dialer(o)
	if err != nil {
		return nil, nil, err
	}
	if o.Host == "" {
		return dialer, nil, nil
	}
	target, err := Target(o)
	if err != nil {
		return nil, nil, err
	}
	return dialer, []session.Target{target}, nil
}

// Target resolves o into a single connection target. It is the CLI's entry
// point; the interactive app uses Session, which wraps it.
func Target(o Options) (session.Target, error) {
	if o.Host == "" {
		return session.Target{}, fmt.Errorf("no host given: use --host or --profile")
	}
	username := o.Username
	if username == "" {
		current, err := user.Current()
		if err != nil {
			return session.Target{}, fmt.Errorf("no --user given and the current user could not be determined: %w", err)
		}
		username = current.Username
	}
	protocol := o.Protocol
	if protocol == "" {
		protocol = session.ProtocolSFTP
	}
	return session.Target{
		Name:      username + "@" + o.Host,
		Protocol:  protocol,
		Host:      o.Host,
		Port:      o.Port,
		User:      username,
		StartPath: o.StartPath,
	}, nil
}

// TargetFromProfile converts a saved profile into a session.Target, normalising
// the host-key policy so a hand-edited config cannot inject an unknown one.
func TargetFromProfile(p config.Profile) session.Target {
	return session.Target{
		Name: p.Name, Protocol: p.Protocol, Host: p.Host,
		Port: p.Port, User: p.User, StartPath: p.StartPath,
		HostKeyPolicy: session.NormalizeHostKeyPolicy(p.HostKeyPolicy),
		// Copied, not shared: the caller's slice must not be written through.
		Bookmarks: append([]string(nil), p.Bookmarks...),
	}
}

// TargetFromProfiles converts a whole list, for the interactive app's profile
// menu. A nil or empty list stays nil, so "no profiles" is not an empty slice.
func TargetFromProfiles(profiles []config.Profile) []session.Target {
	if len(profiles) == 0 {
		return nil
	}
	targets := make([]session.Target, len(profiles))
	for i, p := range profiles {
		targets[i] = TargetFromProfile(p)
	}
	return targets
}

func demoTargets() []session.Target {
	// unreachable.invalid is deliberately absent from the dialer's known
	// hosts, so picking it exercises the connect-failure path by hand.
	return []session.Target{
		{Name: "demo sftp", Protocol: "sftp", Host: "demo-sftp.local", User: "allie", StartPath: "/public_html"},
		{Name: "demo ftps", Protocol: "ftps", Host: "demo-ftps.local", User: "allie"},
		{Name: "unreachable", Protocol: "sftp", Host: "unreachable.invalid", User: "allie"},
	}
}

// demoSession has fake latency so the connecting and loading states are
// visible when running by hand.
func demoSession() session.Dialer {
	return fakesession.New(600*time.Millisecond, 150*time.Millisecond, "demo-sftp.local", "demo-ftps.local")
}
