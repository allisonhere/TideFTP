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
	"tideftp/internal/fakesession"
	"tideftp/internal/ftpsession"
	"tideftp/internal/localfs"
	"tideftp/internal/session"
	"tideftp/internal/sftpsession"
	"tideftp/internal/ui"
)

// version is set via -ldflags "-X main.version=$(VERSION)" at build time.
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.BoolVar(showVersion, "v", false, "print the version and exit")
	host := flag.String("host", "", "host to connect to; without it the app runs on the fake demo adapter")
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
		protocol: *protocol, host: *host, port: *port, username: *username,
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

	program := tea.NewProgram(ui.NewModel(localfs.New(), dialer, targets, cfg, saveConfig), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tideftp: %v\n", err)
		os.Exit(1)
	}
}

// buildSession picks the real SFTP adapter when a host is named, and the demo
// fakes otherwise. There is no form to type a host into yet, so the flags are
// how a real server is reached.
type sessionOptions struct {
	protocol, host       string
	port                 int
	username, startPath  string
	identity, knownHosts string
	ftpsCA               string
	ftpsInsecure         bool
	ftpsAllowTLS13       bool
}

func buildSession(options sessionOptions) (session.Dialer, []session.Target, error) {
	protocol, host := options.protocol, options.host
	username, startPath := options.username, options.startPath
	if host == "" {
		return demoSession(), demoTargets(), nil
	}
	switch protocol {
	case "sftp", "ftp", "ftps":
	default:
		return nil, nil, fmt.Errorf("unknown protocol %q: want sftp, ftp, or ftps", protocol)
	}

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
		StartPath: startPath,
	}

	if protocol == "ftp" || protocol == "ftps" {
		// The password comes from the environment, not a flag: a flag would
		// put it in the process table for every other user on the box to read.
		if os.Getenv(ftpsession.PasswordEnv) == "" {
			return nil, nil, fmt.Errorf("%s protocol needs a password in %s", protocol, ftpsession.PasswordEnv)
		}
		config := ftpsession.Config{
			ExplicitTLS:        protocol == "ftps",
			RootCAFile:         options.ftpsCA,
			InsecureSkipVerify: options.ftpsInsecure,
		}
		if options.ftpsAllowTLS13 {
			config.MaxTLSVersion = tls.VersionTLS13
		}
		return ftpsession.New(config), []session.Target{target}, nil
	}

	// Password auth is offered only if one is in the environment; key-based
	// methods are tried first either way.
	sshConfig := sftpsession.DefaultConfig()
	if options.identity != "" {
		sshConfig.IdentityFiles = []string{options.identity}
		sshConfig.UseAgent = false
	}
	if options.knownHosts != "" {
		sshConfig.KnownHostsPath = options.knownHosts
	}
	return sftpsession.New(sshConfig), []session.Target{target}, nil
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
