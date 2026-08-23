package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"os/user"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/config"
	"tideftp/internal/credstore"
	"tideftp/internal/fakesession"
	"tideftp/internal/ftpsession"
	"tideftp/internal/localfs"
	"tideftp/internal/router"
	"tideftp/internal/session"
	"tideftp/internal/sftpsession"
	"tideftp/internal/ui"
)

// version is set via -ldflags "-X main.version=$(VERSION)" at build time.
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.BoolVar(showVersion, "v", false, "print the version and exit")
	host := flag.String("host", "", "host for an initial target to auto-connect to; without it the app just opens, ready for the connect form")
	demo := flag.Bool("demo", false, "run against the simulated demo adapter instead of real servers, regardless of --host")
	protocol := flag.String("protocol", "sftp", "sftp, ftp, or ftps")
	port := flag.Int("port", 0, "port (default: 22 for sftp, 21 for ftp, 990 for ftps)")
	username := flag.String("user", "", "username (default: the current user)")
	startPath := flag.String("path", "", "remote directory to open on connect")
	identity := flag.String("identity", "", "sftp: SSH private key file; without it the agent and the usual ~/.ssh keys are tried")
	knownHosts := flag.String("known-hosts", "", "sftp: known_hosts file to verify the host key against (default ~/.ssh/known_hosts)")
	ftpsCA := flag.String("ftps-ca", "", "ftps: PEM file to trust in addition to the system roots, for a self-signed server certificate")
	ftpsInsecure := flag.Bool("ftps-insecure", false, "ftps: accept any server certificate (unsafe; prefer --ftps-ca)")
	ftpsTLS13 := flag.Bool("ftps-allow-tls13", false, "ftps: allow TLS 1.3, which some servers mishandle on data connections")
	flag.Parse()

	if *showVersion {
		fmt.Println("tideftp " + version)
		return
	}

	dialer, targets, err := buildSession(sessionOptions{
		demo: *demo, protocol: *protocol, host: *host, port: *port, username: *username,
		startPath: *startPath, identity: *identity, knownHosts: *knownHosts,
		ftpsCA: *ftpsCA, ftpsInsecure: *ftpsInsecure, ftpsAllowTLS13: *ftpsTLS13,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "tideftp: %v\n", err)
		os.Exit(1)
	}

	// Load settings from ~/.config/tideftp/config.toml (or the XDG location),
	// falling back to defaults when the file is absent, corrupt, or unreadable.
	configPath := config.ConfigPath()
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tideftp: warning: could not read %s: %v (using defaults)\n", configPath, err)
		cfg = config.Default()
	}
	saveConfig := func(c config.Config) error { return config.Save(configPath, c) }

	program := tea.NewProgram(ui.NewModel(localfs.New(), dialer, targets, cfg, saveConfig, credstore.New()), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tideftp: %v\n", err)
		os.Exit(1)
	}
}

// buildSession wires up every real protocol adapter behind a router.Dialer,
// unless --demo asks for the simulated fakes instead. All three protocols
// are always dialable — not just the one --protocol names — because the
// connect form lets the user pick any of sftp/ftp/ftps per attempt, for any
// target, not only the one CLI flags describe. --host is likewise optional:
// it only names an initial target to auto-connect to at startup, not a
// requirement for the connect form to be usable — without one, the app just
// opens ready for `c`.
type sessionOptions struct {
	demo                 bool
	protocol, host       string
	port                 int
	username, startPath  string
	identity, knownHosts string
	ftpsCA               string
	ftpsInsecure         bool
	ftpsAllowTLS13       bool
}

func buildSession(options sessionOptions) (session.Dialer, []session.Target, error) {
	if options.demo {
		return demoSession(), demoTargets(), nil
	}

	protocol := options.protocol
	switch protocol {
	case "sftp", "ftp", "ftps":
	default:
		return nil, nil, fmt.Errorf("unknown protocol %q: want sftp, ftp, or ftps", protocol)
	}

	// Password auth for FTP/FTPS is offered only if one is in the
	// environment or typed into the connect form's Password field; key-based
	// methods are tried first for SFTP either way. Nothing here requires a
	// password up front — a target with none configured simply fails to
	// dial until one is supplied, in either place.
	// RootCAFile/InsecureSkipVerify/MaxTLSVersion are TLS-only settings, so
	// plain FTP — no TLS at all — leaves ftpConfig at its zero value.
	ftpConfig := ftpsession.Config{}
	ftpsConfig := ftpsession.Config{
		ExplicitTLS:        true,
		RootCAFile:         options.ftpsCA,
		InsecureSkipVerify: options.ftpsInsecure,
	}
	if options.ftpsAllowTLS13 {
		ftpsConfig.MaxTLSVersion = tls.VersionTLS13
	}

	sshConfig := sftpsession.DefaultConfig()
	if options.identity != "" {
		sshConfig.IdentityFiles = []string{options.identity}
		sshConfig.UseAgent = false
	}
	if options.knownHosts != "" {
		sshConfig.KnownHostsPath = options.knownHosts
	}

	dialer := router.New(map[string]session.Dialer{
		"sftp": sftpsession.New(sshConfig),
		"ftp":  ftpsession.New(ftpConfig),
		"ftps": ftpsession.New(ftpsConfig),
	})

	host := options.host
	if host == "" {
		return dialer, nil, nil
	}

	username := options.username
	if username == "" {
		current, err := user.Current()
		if err != nil {
			return nil, nil, fmt.Errorf("no --user given and the current user could not be determined: %w", err)
		}
		username = current.Username
	}

	target := session.Target{
		Name:      username + "@" + host,
		Protocol:  protocol,
		Host:      host,
		Port:      options.port,
		User:      username,
		StartPath: options.startPath,
	}
	return dialer, []session.Target{target}, nil
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
