package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"tideftp/internal/cli"
	"tideftp/internal/config"
	"tideftp/internal/connect"
	"tideftp/internal/credstore"
	"tideftp/internal/localfs"
	"tideftp/internal/ui"
)

// version is set via -ldflags "-X main.version=$(VERSION)" at build time.
var version = "dev"

// main keeps almost nothing of its own: the work is in run, so that an
// update installed during the session can hand off to the new binary only
// after every defer in run has completed. Exec'ing from inside the program
// would replace the process while Bubble Tea still owned the terminal.
func main() {
	code, restart := run()
	if restart != "" {
		if err := execRestart(restart); err != nil {
			fmt.Fprintf(os.Stderr, "tideftp: could not start the updated binary: %v\n", err)
			os.Exit(1)
		}
	}
	os.Exit(code)
}

// run is everything main used to do. It returns the process exit code and,
// when an update was installed, the path to exec once it has returned.
func run() (code int, restartExec string) {
	// A subcommand (get, put, ls, ...) runs headlessly and exits; anything
	// else — no argument, or flags alone — still opens the interactive app.
	// This must come before flag.Parse, which would reject the subcommand's own
	// flags as unknown.
	if cli.IsCommand(os.Args) {
		return cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr), ""
	}

	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.BoolVar(showVersion, "v", false, "print the version and exit")
	host := flag.String("host", "", "host for an initial target to auto-connect to; without it the app just opens, ready for the connect form")
	demo := flag.Bool("demo", false, "run against the simulated demo adapter instead of real servers, regardless of --host")
	transferLab := flag.Bool("transfer-lab", false, "enable developer-only deterministic transfer scenarios (uses the demo adapter)")
	protocol := flag.String("protocol", "sftp", "sftp, ftp, ftps (explicit, AUTH TLS), or ftps-implicit")
	port := flag.Int("port", 0, "port (default: 22 for sftp, 21 for ftp and ftps, 990 for ftps-implicit)")
	username := flag.String("user", "", "username (default: the current user)")
	startPath := flag.String("path", "", "remote directory to open on connect")
	identity := flag.String("identity", "", "sftp: SSH private key file; without it the agent and the usual ~/.ssh keys are tried")
	knownHosts := flag.String("known-hosts", "", "sftp: known_hosts file to verify the host key against (default ~/.ssh/known_hosts)")
	ftpsCA := flag.String("ftps-ca", "", "ftps/ftps-implicit: PEM file to trust in addition to the system roots, for a self-signed server certificate")
	ftpsInsecure := flag.Bool("ftps-insecure", false, "ftps/ftps-implicit: accept any server certificate (unsafe; prefer --ftps-ca)")
	ftpsTLS13 := flag.Bool("ftps-allow-tls13", false, "ftps/ftps-implicit: allow TLS 1.3, which some servers mishandle on data connections")
	flag.Parse()

	if *showVersion {
		fmt.Println("tideftp " + version)
		return 0, ""
	}

	// internal/ui and tideui both render through lipgloss's shared global
	// renderer, which by default auto-detects the color profile from TERM/
	// COLORTERM — a common source of false negatives (tmux, some SSH
	// sessions, terminals that support truecolor but never set COLORTERM)
	// that would otherwise quietly clip the app's 24-bit gradient colors
	// (the Stats tab's throughput line, tideui's modal shadow blending) down
	// to the nearest ANSI256 entry. Forcing it here trusts that the
	// deployment target actually is truecolor-capable rather than whatever
	// termenv's heuristics conclude; NO_COLOR is still honored for anyone
	// who explicitly asks for no color at all.
	if os.Getenv("NO_COLOR") == "" {
		lipgloss.SetColorProfile(termenv.TrueColor)
	}

	dialer, targets, err := connect.Session(connect.Options{
		Demo: *demo || *transferLab, Protocol: *protocol, Host: *host, Port: *port, Username: *username,
		StartPath: *startPath, Identity: *identity, KnownHosts: *knownHosts,
		FTPSCA: *ftpsCA, FTPSInsecure: *ftpsInsecure, FTPSAllowTLS13: *ftpsTLS13,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "tideftp: %v\n", err)
		return 1, ""
	}

	// Load settings from ~/.config/tideftp/config.toml (or the XDG location),
	// falling back to defaults when the file is absent, corrupt, or unreadable.
	configPath := config.ConfigPath()
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tideftp: warning: could not read %s: %v (using defaults)\n", configPath, err)
		cfg = config.Default()
		if errors.Is(err, config.ErrCorrupt) {
			// The file is there and holds the user's saved profiles; it just
			// does not parse. Starting on defaults is fine, but the first
			// settings change would then write those defaults straight over
			// the profiles — so the unreadable file is moved aside first and
			// this run starts a new one. Nothing is deleted; the old content
			// is one rename away.
			backup := configPath + ".corrupt"
			if renameErr := os.Rename(configPath, backup); renameErr != nil {
				fmt.Fprintf(os.Stderr, "tideftp: could not set %s aside (%v) — settings will not be saved this run\n", configPath, renameErr)
				configPath = ""
			} else {
				fmt.Fprintf(os.Stderr, "tideftp: moved it to %s; your saved profiles are still in there\n", backup)
			}
		}
	}
	var saveConfig config.SaveFunc
	if configPath != "" {
		saveConfig = func(c config.Config) error { return config.Save(configPath, c) }
	}

	model := ui.NewModel(localfs.New(), dialer, targets, cfg, saveConfig, credstore.New(), version)
	if *transferLab {
		model.EnableTransferLab()
	}
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	final, err := program.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tideftp: %v\n", err)
		return 1, ""
	}
	if model, ok := final.(ui.Model); ok {
		restartExec = model.RestartExecPath()
	}
	return 0, restartExec
}
