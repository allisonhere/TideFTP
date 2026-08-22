package main

import (
	"flag"
	"fmt"
	"os"
	"os/user"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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
	flag.Parse()

	if *showVersion {
		fmt.Println("tideftp " + version)
		return
	}

	dialer, targets, err := buildSession(*protocol, *host, *port, *username, *startPath, *identity, *knownHosts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tideftp: %v\n", err)
		os.Exit(1)
	}

	program := tea.NewProgram(ui.NewModel(localfs.New(), dialer, targets), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tideftp: %v\n", err)
		os.Exit(1)
	}
}

// buildSession picks the real SFTP adapter when a host is named, and the demo
// fakes otherwise. There is no form to type a host into yet, so the flags are
// how a real server is reached.
func buildSession(protocol, host string, port int, username, startPath, identity, knownHosts string) (session.Dialer, []session.Target, error) {
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
		Port:      port,
		User:      username,
		StartPath: startPath,
	}

	if protocol == "ftp" || protocol == "ftps" {
		// The password comes from the environment, not a flag: a flag would
		// put it in the process table for every other user on the box to read.
		if os.Getenv(ftpsession.PasswordEnv) == "" {
			return nil, nil, fmt.Errorf("%s protocol needs a password in %s", protocol, ftpsession.PasswordEnv)
		}
		return ftpsession.New(ftpsession.Config{ExplicitTLS: protocol == "ftps"}), []session.Target{target}, nil
	}

	// Password auth is offered only if one is in the environment; key-based
	// methods are tried first either way.
	config := sftpsession.DefaultConfig()
	if identity != "" {
		config.IdentityFiles = []string{identity}
		config.UseAgent = false
	}
	if knownHosts != "" {
		config.KnownHostsPath = knownHosts
	}
	return sftpsession.New(config), []session.Target{target}, nil
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
