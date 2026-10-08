// Package cli is TideFTP's non-interactive mode: connect, act, print a result,
// exit. It is deliberately free of any UI framework — the interactive app in
// internal/ui drives the same adapters, but this package is what a shell
// script, a CI job or a cron entry calls.
//
// One invocation is one command (ls, get, put, rm, mkdir, mv), one connection,
// then exit. The shell is the scripting language; there is deliberately no
// session DSL. Credentials are never flags: a password comes from
// TIDEFTP_SFTP_PASSWORD / TIDEFTP_FTP_PASSWORD, or the OS keyring when a saved
// profile is named.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"tideftp/internal/config"
	"tideftp/internal/connect"
	"tideftp/internal/credstore"
	"tideftp/internal/session"
)

// dialTimeout bounds the connect handshake. A listing or transfer after a
// successful dial is the command's own business.
const dialTimeout = 60 * time.Second

// IsCommand reports whether argv (the full os.Args, including the program
// name) should be handled non-interactively: a first argument that is not a
// flag. main uses it to choose the CLI path before the flag package would
// treat the command word as a stray positional. An unrecognised word is left
// to App.Run, which reports it as an unknown command — the interactive app
// never used positional arguments, so nothing is taken away.
func IsCommand(argv []string) bool {
	return len(argv) > 1 && argv[1] != "" && argv[1][0] != '-'
}

// App holds the process boundaries and the two things that reach outside the
// process, so a test can drive the CLI with neither a terminal nor a server.
type App struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Dial builds the protocol router for resolved options. Tests replace it
	// with a fake.
	Dial func(connect.Options) (session.Dialer, error)
	// shared, when set, is the one connection a script or shell runs every
	// command over; open hands it out instead of dialling.
	shared *sharedSession
	// Sleep waits between connect retries. Nil means time.Sleep; tests replace
	// it so a retry does not slow them down.
	Sleep func(time.Duration)
	// Keyring reads a remembered password for a named profile. Nil disables it.
	Keyring credstore.Store
	// ConfigPath is where profiles are read from. Empty means the user's usual
	// config location.
	ConfigPath string
}

// Run builds the real App and executes one subcommand, returning the process
// exit code: 0 success, 1 an operation failed, 2 a usage or connection error.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	app := App{
		Stdin:   stdin,
		Stdout:  stdout,
		Stderr:  stderr,
		Dial:    connect.Dialer,
		Keyring: credstore.New(),
	}
	return app.Run(args)
}

// errUsage marks an error as the caller's mistake, so Run can exit 2 rather
// than 1.
var errUsage = errors.New("usage")

func usageError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errUsage, fmt.Sprintf(format, args...))
}

// Run dispatches one subcommand and maps its error to an exit code.
func (a App) Run(args []string) int {
	if len(args) == 0 {
		a.usage()
		return 2
	}
	cmd, rest := args[0], args[1:]
	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		a.usage()
		return 0
	}
	known, err := a.dispatch(cmd, rest)
	if !known {
		_, _ = fmt.Fprintf(a.Stderr, "tideftp: unknown command %q\n\n", cmd)
		a.usage()
		return exitUsage
	}
	return a.report(err)
}

// dispatch runs one subcommand. known is false for a command word the CLI
// does not have. It is shared by Run and the script/shell runner.
func (a App) dispatch(cmd string, rest []string) (known bool, err error) {
	switch cmd {
	case "ls":
		err = a.cmdLs(rest)
	case "get":
		err = a.cmdGet(rest)
	case "put":
		err = a.cmdPut(rest)
	case "rm":
		err = a.cmdRm(rest)
	case "mkdir":
		err = a.cmdMkdir(rest)
	case "mv":
		err = a.cmdMv(rest)
	case "sync", "mirror":
		err = a.cmdSync(rest)
	case "stat":
		err = a.cmdStat(rest)
	case "exists":
		err = a.cmdExists(rest)
	case "cat":
		err = a.cmdCat(rest)
	case "du":
		err = a.cmdDu(rest)
	case "find":
		err = a.cmdFind(rest)
	case "tree":
		err = a.cmdTree(rest)
	case "chmod":
		err = a.cmdChmod(rest)
	case "script":
		err = a.cmdScript(rest, false)
	case "shell":
		err = a.cmdScript(rest, true)
	default:
		return false, nil
	}
	return true, err
}

// report prints err the way the CLI always has and returns its exit code.
func (a App) report(err error) int {
	if err == nil {
		return exitOK
	}
	var untrusted *session.UntrustedHostKeyError
	switch {
	case errors.As(err, &untrusted):
		_, _ = fmt.Fprintf(a.Stderr, "tideftp: %v\n", err)
		_, _ = fmt.Fprintf(a.Stderr, "  the host key is not in known_hosts; verify it and add it, or pass --host-key-policy off to accept it\n")
		return exitConnect
	case errors.Is(err, errUsage):
		// A bare errUsage means the flag package already reported it; only a
		// wrapped usageError (with a reason) needs printing here.
		if err.Error() != errUsage.Error() {
			_, _ = fmt.Fprintf(a.Stderr, "tideftp: %v\n", err)
		}
	case err.Error() != "":
		_, _ = fmt.Fprintf(a.Stderr, "tideftp: %v\n", err)
	}
	return exitCode(err)
}

func (a App) statusf(format string, args ...any) {
	_, _ = fmt.Fprintf(a.Stderr, format+"\n", args...)
}

func (a App) resultf(format string, args ...any) {
	_, _ = fmt.Fprintf(a.Stdout, format+"\n", args...)
}

// newFlagSet builds a parsable flag set that reports errors instead of exiting,
// so a bad flag becomes exit code 2 and stays testable.
func (a App) newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("tideftp "+name, flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(a.Stderr, "usage: tideftp %s [flags] %s\n", name, operandHint(name))
		fs.PrintDefaults()
	}
	return fs
}

func operandHint(name string) string {
	switch name {
	case "ls":
		return "[PATH]"
	case "get":
		return "REMOTE [LOCAL]"
	case "put":
		return "LOCAL [REMOTE]"
	case "rm":
		return "PATH..."
	case "mkdir":
		return "PATH..."
	case "mv":
		return "OLD NEW"
	case "sync":
		return "SRC DST"
	default:
		return ""
	}
}

// connFlags is the connection half of every subcommand's flag set. Protocol
// defaults to empty rather than "sftp" so that --profile's protocol is not
// silently overridden; connect.Target resolves the empty default to sftp.
type connFlags struct {
	profile       string
	protocol      string
	host          string
	port          int
	user          string
	path          string
	identity      string
	knownHosts    string
	ftpsCA        string
	ftpsInsecure  bool
	ftpsTLS13     bool
	hostKeyPolicy string
	quiet         bool
	passwordStdin bool
	noPart        bool
	retries       int
	timeout       time.Duration
}

func (c *connFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.profile, "profile", "", "saved profile name from config.toml")
	fs.StringVar(&c.protocol, "protocol", "", "sftp, ftp, ftps, or ftps-implicit (default sftp)")
	fs.StringVar(&c.host, "host", "", "server to connect to")
	fs.IntVar(&c.port, "port", 0, "port (default: 22 sftp, 21 ftp/ftps, 990 ftps-implicit)")
	fs.StringVar(&c.user, "user", "", "username (default: the current user)")
	fs.StringVar(&c.path, "path", "", "remote base directory (default /)")
	fs.StringVar(&c.identity, "identity", "", "sftp: SSH private key file")
	fs.StringVar(&c.knownHosts, "known-hosts", "", "sftp: known_hosts file to verify against")
	fs.StringVar(&c.ftpsCA, "ftps-ca", "", "ftps: PEM file to trust in addition to the system roots")
	fs.BoolVar(&c.ftpsInsecure, "ftps-insecure", false, "ftps: accept any server certificate (unsafe)")
	fs.BoolVar(&c.ftpsTLS13, "ftps-allow-tls13", false, "ftps: allow TLS 1.3, which some servers mishandle")
	fs.StringVar(&c.hostKeyPolicy, "host-key-policy", "", "sftp: strict (default) or off")
	fs.BoolVar(&c.quiet, "quiet", false, "print only errors")
	fs.BoolVar(&c.quiet, "q", false, "print only errors")
	fs.BoolVar(&c.passwordStdin, "password-stdin", false, "read the password from the first line of standard input")
	fs.IntVar(&c.retries, "retries", 0, "retry a failed connection this many times, with backoff")
	fs.DurationVar(&c.timeout, "timeout", dialTimeout, "give up connecting after this long (per attempt)")
}

func (a App) loadConfig() (config.Config, error) {
	path := a.ConfigPath
	if path == "" {
		path = config.ConfigPath()
	}
	return config.Load(path)
}

// target resolves the connection flags — or a named saved profile plus any
// overrides — into where to connect.
func (a App) target(c *connFlags) (session.Target, error) {
	var target session.Target
	if c.profile != "" {
		cfg, err := a.loadConfig()
		if err != nil {
			return session.Target{}, fmt.Errorf("read config: %w", err)
		}
		var found *config.Profile
		for i := range cfg.Profiles {
			if cfg.Profiles[i].Name == c.profile {
				found = &cfg.Profiles[i]
				break
			}
		}
		if found == nil {
			return session.Target{}, fmt.Errorf("no saved profile named %q", c.profile)
		}
		target = connect.TargetFromProfile(*found)
		// A flag overrides the profile only when it was actually given, which
		// is why the protocol default is empty all the way down.
		if c.host != "" {
			target.Host = c.host
		}
		if c.user != "" {
			target.User = c.user
		}
		if c.port != 0 {
			target.Port = c.port
		}
		if c.protocol != "" {
			target.Protocol = c.protocol
		}
		if c.path != "" {
			target.StartPath = c.path
		}
		target.Name = target.User + "@" + target.Host
		if target.Protocol == "" {
			target.Protocol = session.ProtocolSFTP
		}
	} else {
		t, err := connect.Target(connect.Options{
			Protocol: c.protocol, Host: c.host, Port: c.port,
			Username: c.user, StartPath: c.path,
		})
		if err != nil {
			return session.Target{}, usageError("%v", err)
		}
		target = t
	}

	// A non-interactive run cannot answer a host-key prompt, so "ask" becomes
	// strict. An explicit --host-key-policy, or a profile that already says
	// off, is honoured.
	policy := c.hostKeyPolicy
	if policy == "" {
		policy = target.HostKeyPolicy
	}
	if policy != session.HostKeyOff {
		policy = session.HostKeyStrict
	}
	target.HostKeyPolicy = policy
	return target, nil
}

// credentials picks the password: standard input when --password-stdin is
// set (which also forces password auth for SFTP, since that is plainly what
// was asked for), else the keyring entry for a saved profile.
func (a App) credentials(target session.Target, c *connFlags) (session.Credentials, error) {
	if c.passwordStdin {
		password, err := readPasswordStdin(a.Stdin)
		if err != nil {
			return session.Credentials{}, err
		}
		return session.Credentials{
			Password:     password,
			PasswordOnly: target.Protocol == session.ProtocolSFTP,
		}, nil
	}
	if a.Keyring == nil {
		return session.Credentials{}, nil
	}
	key := credstore.Key(target.Protocol, target.Host, target.Port, target.User)
	password, ok, err := a.Keyring.Get(key)
	if err != nil || !ok {
		return session.Credentials{}, nil
	}
	return session.Credentials{Password: password}, nil
}

func (a App) dial(target session.Target, creds session.Credentials, c *connFlags) (session.Conn, error) {
	dialer, err := a.Dial(connect.Options{
		Protocol: target.Protocol, Identity: c.identity, KnownHosts: c.knownHosts,
		FTPSCA: c.ftpsCA, FTPSInsecure: c.ftpsInsecure, FTPSAllowTLS13: c.ftpsTLS13,
	})
	if err != nil {
		return nil, err
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = dialTimeout
	}
	sleep := a.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	for attempt := 0; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		conn, err := dialer.Dial(ctx, target, creds)
		cancel()
		if err == nil {
			return conn, nil
		}
		err = classifyDial(err)
		// Only a connection failure is worth retrying: a wrong password or an
		// unknown host key will fail identically every time.
		if exitCode(err) != exitConnect || errors.As(err, new(*session.UntrustedHostKeyError)) || attempt >= c.retries {
			return nil, err
		}
		wait := min(time.Second<<attempt, 30*time.Second)
		if !c.quiet {
			a.statusf("connect failed (%v); retrying in %s (%d/%d)", err, wait, attempt+1, c.retries)
		}
		sleep(wait)
	}
}

func (a App) usage() {
	_, _ = fmt.Fprint(a.Stdout, `tideftp — non-interactive file transfer

Usage: tideftp <command> [flags] [operands]

Commands:
  ls    [flags] [PATH]          list a remote directory, or one path
  get   [flags] REMOTE [LOCAL]  download a file (or -r a directory)
  put   [flags] LOCAL [REMOTE]  upload a file (or -r a directory)
  rm    [flags] PATH...         delete files (or -r directories)
  mkdir [flags] PATH...         create directories
  mv    OLD NEW                 rename a remote path
  sync  [flags] SRC DST         one-way mirror; SRC/DST is a local dir or
                                PROFILE:/path (or :/path using --host...)
  mirror                        alias for sync
  cat   [flags] PATH...         write remote files to stdout
  du    [-h] [-d N] PATH...     total size of a path (per directory with -d)
  find  [flags] [PATH]          list paths by name, type, size or age
  tree  [-L N] [PATH]           indented directory tree
  chmod [-R] MODE PATH...       set permissions (755, u+x,go-w)
  script [-c CMDS] [FILE]       run many commands over ONE connection
  shell                         the same, interactively (prompt on stderr)
  stat  [flags] PATH...         show type, size, mode, mtime (--json)
  exists [-f|-d] PATH...        exit 0 if every PATH exists, 5 if not

REMOTE operands may end in a wildcard (/logs/*.gz); get, put and rm accept
several sources, with the last operand as the destination directory.

Connection flags (on every command):
  --profile NAME   use a saved profile from config.toml
  --host HOST      server to connect to (or --profile)
  --protocol P     sftp (default), ftp, ftps, ftps-implicit
  --port N --user U --path DIR
  --identity FILE --known-hosts FILE --host-key-policy strict|off
  --ftps-ca FILE --ftps-insecure --ftps-allow-tls13
  -q, --quiet      print only errors
  --password-stdin read the password from the first line of stdin
  --retries N      retry a failed connection N times (1s, 2s, 4s... backoff)
  --timeout D      connect timeout per attempt (default 1m0s)

Exit codes: 0 ok, 1 failed, 2 usage, 3 connection, 4 authentication, 5 not found.

Passwords are never flags: set TIDEFTP_SFTP_PASSWORD or TIDEFTP_FTP_PASSWORD,
or save one on a profile (it is read from the OS keyring).

Examples:
  tideftp ls --host files.example.com /var/www
  tideftp get --profile prod /var/log/app.log ./logs/
  TIDEFTP_FTP_PASSWORD=... tideftp put --protocol ftps --host ftp.example.com site.tar /pub/
  tideftp get -r --host files.example.com /var/www ./site
`)
}
