package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"tideftp/internal/config"
	"tideftp/internal/session"
	"tideftp/internal/transfer"
)

// sharedSession is the connection a script or shell runs over, plus the
// session state `cd` and `set` change. A background job gets a copy of its own
// with a connection of its own.
type sharedSession struct {
	conn   session.Conn // nil until `open` when the script started unconnected
	target session.Target
	cwd    string
	quiet  bool
	limit  *transfer.Limiter
	creds  session.Credentials
	flags  *connFlags
	// settings are the lftp-style `set NAME VALUE` values commands consult.
	settings *settings
}

// keepOpen stops a command's deferred Close from ending the shared
// connection; the script runner closes it once, at the end.
type keepOpen struct{ session.Conn }

func (keepOpen) Close() error { return nil }

// settings is the table behind `set`. It is shared by a script and its jobs.
type settings struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *settings) get(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[name]
	return v, ok
}

// knownSettings are the names `set` accepts, with what each one does.
var knownSettings = map[string]string{
	"net:limit-rate":              "cap transfer speed in bytes/s (500k, 2M); 0 = unlimited",
	"net:limit-total-rate":        "same as net:limit-rate (one limiter is shared by all parallel files)",
	"net:max-retries":             "reconnects allowed per operation; 0 = none, -1 = unlimited",
	"net:reconnect-interval-base": "first reconnect delay in seconds (doubles each time, capped at 30s)",
	"net:timeout":                 "connect timeout per attempt, e.g. 30s",
	"mirror:parallel":             "default --parallel for sync and mirror",
	"cmd:fail-exit":               "yes: stop the script at the first failing command; no: keep going",
}

// splitCommands tokenizes one line of script into commands. Words are split
// on whitespace; single quotes keep everything literal; double quotes allow
// \" and \\ ; a backslash outside quotes escapes the next character. An
// unquoted ; or newline ends a command and an unquoted # at the start of a
// word begins a comment that runs to the end of the line.
func splitCommands(line string) ([][]string, error) {
	cmds, err := parseScript(line)
	if err != nil {
		return nil, err
	}
	var out [][]string
	for _, c := range cmds {
		out = append(out, c.words)
	}
	return out, nil
}

// scriptCmd is one parsed command; bg is set by a trailing unquoted &.
type scriptCmd struct {
	words []string
	bg    bool
}

func parseScript(line string) ([]scriptCmd, error) {
	var cmds []scriptCmd
	var cur []string
	var word strings.Builder
	inWord := false
	flushWord := func() {
		if inWord {
			cur = append(cur, word.String())
			word.Reset()
			inWord = false
		}
	}
	endCmd := func(bg bool) {
		flushWord()
		if len(cur) > 0 {
			cmds = append(cmds, scriptCmd{words: cur, bg: bg})
			cur = nil
		}
	}
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		ch := rs[i]
		switch {
		case ch == '\'':
			inWord = true
			i++
			for ; i < len(rs) && rs[i] != '\''; i++ {
				word.WriteRune(rs[i])
			}
			if i >= len(rs) {
				return nil, errors.New("unterminated single quote")
			}
		case ch == '"':
			inWord = true
			i++
			for ; i < len(rs) && rs[i] != '"'; i++ {
				if rs[i] == '\\' && i+1 < len(rs) && (rs[i+1] == '"' || rs[i+1] == '\\') {
					i++
				}
				word.WriteRune(rs[i])
			}
			if i >= len(rs) {
				return nil, errors.New("unterminated double quote")
			}
		case ch == '\\':
			if i+1 >= len(rs) {
				return nil, errors.New("trailing backslash")
			}
			i++
			inWord = true
			word.WriteRune(rs[i])
		case ch == '#' && !inWord:
			endCmd(false)
			return cmds, nil
		case ch == '&':
			if i+1 < len(rs) && rs[i+1] == '&' {
				return nil, errors.New("&& is not supported; put commands on separate lines")
			}
			if !inWord && len(cur) == 0 {
				return nil, errors.New("& must follow a command")
			}
			endCmd(true)
		case ch == ';' || ch == '\n':
			endCmd(false)
		case ch == ' ' || ch == '\t' || ch == '\r':
			flushWord()
		default:
			inWord = true
			word.WriteRune(ch)
		}
	}
	endCmd(false)
	return cmds, nil
}

// scriptRun is one script/shell invocation.
type scriptRun struct {
	app         App // app.shared is this script's own session
	keepGoing   bool
	echo        bool
	interactive bool
	failCode    int // first non-zero exit code seen
	depth       int // source nesting

	aliases map[string][]string
	jobs    *jobTable
}

// errScriptExit ends a script early; code is the exit code asked for.
type errScriptExit struct {
	code int
	kill bool
}

func (e errScriptExit) Error() string { return "" }

func (a App) cmdScript(args []string, interactive bool) error {
	name := "script"
	if interactive {
		name = "shell"
	}
	fset := a.newFlagSet(name)
	var inline, rcFile string
	var keepGoing, echo, noRC bool
	if !interactive {
		fset.StringVar(&inline, "c", "", "run these commands (separated by ; or newlines) instead of reading a file")
	}
	fset.BoolVar(&keepGoing, "k", interactive, "keep going after a command fails (the exit code is the first failure's)")
	fset.BoolVar(&echo, "x", false, "print each command to stderr before running it")
	fset.StringVar(&rcFile, "rc", "", "read set/alias commands from this file first (default: cli.rc beside config.toml)")
	fset.BoolVar(&noRC, "no-rc", false, "do not read the rc file")
	c := &connFlags{}
	c.register(fset)
	if err := fset.Parse(args); err != nil {
		return errUsage
	}

	var src io.Reader
	fromStdin := false
	switch {
	case inline != "":
		if fset.NArg() > 0 {
			return usageError("script takes -c COMMANDS or a FILE, not both")
		}
		src = strings.NewReader(inline)
	case fset.NArg() > 1:
		return usageError("script takes at most one FILE")
	case fset.NArg() == 1 && fset.Arg(0) != "-":
		f, err := os.Open(fset.Arg(0))
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		src = f
	default:
		src, fromStdin = a.Stdin, true
	}
	if fromStdin && c.passwordStdin {
		return usageError("--password-stdin and a script on stdin both read standard input; give the script as a FILE or -c")
	}

	defaultRetries(fset, c, 3)
	if err := a.resolveLimit(c); err != nil {
		return err
	}
	sh := &sharedSession{quiet: c.quiet, limit: c.limit, flags: c, cwd: "/",
		settings: &settings{m: map[string]string{}}}

	// A script may start unconnected and `open` a server itself.
	if c.host != "" || c.profile != "" {
		target, err := a.target(c)
		if err != nil {
			return err
		}
		creds, err := a.credentials(target, c)
		if err != nil {
			return err
		}
		conn, err := a.connectLive(target, creds, c)
		if err != nil {
			return err
		}
		sh.conn, sh.target, sh.creds, sh.cwd = conn, target, creds, target.Home()
	}
	defer func() {
		if sh.conn != nil {
			_ = sh.conn.Close()
		}
	}()

	b := a
	b.shared = sh
	run := &scriptRun{app: b, keepGoing: keepGoing, echo: echo, interactive: interactive,
		aliases: map[string][]string{}, jobs: newJobTable()}
	if err := run.loadRC(rcFile, noRC); err != nil {
		return err
	}
	err := run.runReader(src, "script")
	return run.finish(err)
}

// connectLive dials target and wraps the connection so it redials and resumes.
func (a App) connectLive(target session.Target, creds session.Credentials, c *connFlags) (session.Conn, error) {
	dial := func() (session.Conn, error) { return a.dial(target, creds, c) }
	first, err := dial()
	if err != nil {
		return nil, err
	}
	return a.live(first, dial, c), nil
}

// loadRC runs the rc file's set/alias lines. Anything that would touch the
// network is refused, so a stray rc line cannot connect anywhere by itself.
func (s *scriptRun) loadRC(explicit string, skip bool) error {
	if skip {
		return nil
	}
	path := explicit
	if path == "" {
		cfg := s.app.ConfigPath
		if cfg == "" {
			cfg = config.ConfigPath()
		}
		path = filepath.Join(filepath.Dir(cfg), "cli.rc")
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) && explicit == "" {
			return nil
		}
		return err
	}
	defer func() { _ = f.Close() }()
	reader := bufio.NewReader(f)
	for n := 1; ; n++ {
		line, rerr := reader.ReadString('\n')
		if rerr != nil && line == "" {
			return nil
		}
		cmds, perr := parseScript(line)
		if perr != nil {
			return fmt.Errorf("%s:%d: %w", path, n, perr)
		}
		for _, c := range cmds {
			switch c.words[0] {
			case "set", "alias", "echo", "source":
			default:
				return fmt.Errorf("%s:%d: only set, alias, echo and source are allowed in an rc file (got %q)", path, n, c.words[0])
			}
			if err := s.runOne(s.app, c.words[0], c.words[1:]); err != nil {
				return fmt.Errorf("%s:%d: %w", path, n, err)
			}
		}
	}
}

// runReader runs every line of src.
func (s *scriptRun) runReader(src io.Reader, label string) error {
	reader := bufio.NewReader(src)
	for lineNo := 1; ; lineNo++ {
		if s.interactive && s.depth == 0 {
			_, _ = fmt.Fprintf(s.app.Stderr, "tideftp %s> ", s.app.shared.cwd)
		}
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			break
		}
		// A trailing backslash joins the next line.
		for strings.HasSuffix(strings.TrimRight(line, "\r\n"), "\\") && !s.interactive {
			next, nerr := reader.ReadString('\n')
			line = strings.TrimSuffix(strings.TrimRight(line, "\r\n"), "\\") + " " + next
			if nerr != nil {
				break
			}
		}
		stop, err := s.runLine(line, fmt.Sprintf("%s:%d", label, lineNo))
		if err != nil {
			return err
		}
		if stop {
			break
		}
	}
	return nil
}

// runLine runs the commands on one line. stop is true when the script should
// end.
func (s *scriptRun) runLine(line, where string) (stop bool, err error) {
	if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "!") {
		// `!` hands the whole rest of the line to the local shell.
		return s.handle(s.runShell(strings.TrimSpace(strings.TrimPrefix(trimmed, "!"))), 0)
	}
	cmds, perr := parseScript(line)
	if perr != nil {
		return s.handle(fmt.Errorf("%s: %w", where, perr), exitUsage)
	}
	for _, c := range cmds {
		if s.echo {
			s.app.statusf("+ %s", strings.Join(c.words, " "))
		}
		var cerr error
		if c.bg {
			cerr = s.startJob(c.words)
		} else {
			cerr = s.runOne(s.app, c.words[0], c.words[1:])
		}
		if stop, err := s.handle(cerr, 0); stop || err != nil {
			return stop, err
		}
	}
	return false, nil
}

func (s *scriptRun) handle(cerr error, code int) (stop bool, err error) {
	if cerr == nil {
		return false, nil
	}
	var exit errScriptExit
	if errors.As(cerr, &exit) {
		if exit.kill {
			s.jobs.killAll()
		}
		if exit.code != 0 && s.failCode == 0 {
			s.failCode = exit.code
		}
		return true, nil
	}
	reported := s.app.report(cerr)
	if code == 0 {
		code = reported
	}
	if s.failCode == 0 {
		s.failCode = code
	}
	return !s.keepGoing, nil
}

// finish waits for background jobs and turns the first failure into the exit
// code.
func (s *scriptRun) finish(err error) error {
	if err != nil {
		return err
	}
	if code := s.jobs.waitAll(s.app); code != 0 && s.failCode == 0 {
		s.failCode = code
	}
	if s.failCode != 0 {
		return &codedError{code: s.failCode}
	}
	return nil
}

func (s *scriptRun) runShell(cmdline string) error {
	if cmdline == "" {
		return usageError("! needs a command")
	}
	cmd := exec.Command("sh", "-c", cmdline)
	cmd.Stdout, cmd.Stderr = lockedWriter{s.app.Stdout}, lockedWriter{s.app.Stderr}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("!%s: %w", cmdline, err)
	}
	return nil
}

// runOne runs a builtin or a regular subcommand against app's session.
func (s *scriptRun) runOne(app App, cmd string, args []string) error {
	sh := app.shared
	if repl, ok := s.aliases[cmd]; ok && len(repl) > 0 {
		cmd, args = repl[0], append(append([]string{}, repl[1:]...), args...)
	}
	switch cmd {
	case "exit", "quit":
		e := errScriptExit{}
		for _, a := range args {
			switch a {
			case "kill":
				e.kill = true
			case "bg":
			default:
				n, err := strconv.Atoi(a)
				if err != nil {
					return usageError("exit takes a number, bg or kill")
				}
				e.code = n
			}
		}
		return e
	case "pwd":
		app.resultf("%s", sh.cwd)
		return nil
	case "lpwd":
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		app.resultf("%s", wd)
		return nil
	case "cd":
		if len(args) > 1 {
			return usageError("cd takes one directory")
		}
		if sh.conn == nil {
			return usageError("not connected: use open HOST|PROFILE first")
		}
		dir := sh.target.Home()
		if len(args) == 1 {
			dir = resolveRemote(sh.cwd, args[0])
		}
		entry, err := sh.conn.FS().Stat(context.Background(), dir)
		if err != nil {
			return err
		}
		if !entry.IsDirLike() {
			return usageError("%s is not a directory", dir)
		}
		sh.cwd = dir
		return nil
	case "lcd":
		if len(args) != 1 {
			return usageError("lcd takes one directory")
		}
		return os.Chdir(args[0])
	case "echo":
		noNL := len(args) > 0 && args[0] == "-n"
		if noNL {
			args = args[1:]
		}
		outMu.Lock()
		_, _ = fmt.Fprint(app.Stdout, strings.Join(args, " "))
		if !noNL {
			_, _ = fmt.Fprintln(app.Stdout)
		}
		outMu.Unlock()
		return nil
	case "set":
		return s.cmdSet(app, args)
	case "alias":
		return s.cmdAlias(app, args)
	case "source":
		return s.cmdSource(args)
	case "open":
		return s.cmdOpen(app, args)
	case "close":
		return s.closeConn(sh)
	case "jobs":
		s.jobs.list(app)
		return nil
	case "wait":
		return s.jobs.wait(app, args)
	case "kill":
		return s.jobs.kill(args)
	case "queue":
		return s.queueJob(args)
	case "script", "shell":
		return usageError("scripts cannot be nested")
	case "help":
		if len(args) == 0 {
			app.usage()
			return nil
		}
		return helpFor(app, args[0])
	}
	known, err := app.dispatch(cmd, args)
	if !known {
		return usageError("unknown command %q", cmd)
	}
	return err
}

// helpFor prints a command's flags by asking its flag set for -h.
func helpFor(app App, cmd string) error {
	switch cmd {
	case "set", "alias", "source", "open", "close", "jobs", "wait", "kill", "queue", "echo", "exit", "cd", "lcd", "pwd", "lpwd":
		app.statusf("%s is a script/shell builtin; see the README's scripts and shell section", cmd)
		return nil
	}
	known, err := app.dispatch(cmd, []string{"-h"})
	if !known {
		return usageError("no help for unknown command %q", cmd)
	}
	if errors.Is(err, errUsage) {
		return nil // -h printing usage is the point
	}
	return err
}

func (s *scriptRun) cmdSet(app App, args []string) error {
	sh := app.shared
	if len(args) == 0 {
		names := make([]string, 0, len(knownSettings))
		for n := range knownSettings {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			v, _ := sh.settings.get(n)
			app.resultf("%-28s %-10s %s", n, v, knownSettings[n])
		}
		return nil
	}
	name := args[0]
	if _, ok := knownSettings[name]; !ok {
		return usageError("unknown setting %q (run `set` to list them)", name)
	}
	if len(args) == 1 {
		v, _ := sh.settings.get(name)
		app.resultf("%s %s", name, v)
		return nil
	}
	if len(args) > 2 {
		return usageError("set takes NAME VALUE")
	}
	value := args[1]
	if err := s.applySetting(sh, name, value); err != nil {
		return usageError("set %s: %v", name, err)
	}
	sh.settings.mu.Lock()
	sh.settings.m[name] = value
	sh.settings.mu.Unlock()
	return nil
}

func (s *scriptRun) applySetting(sh *sharedSession, name, value string) error {
	switch name {
	case "net:limit-rate", "net:limit-total-rate":
		rate, err := parseSize(value)
		if err != nil {
			return err
		}
		sh.limit = transfer.NewLimiter(rate)
	case "net:max-retries":
		n, err := strconv.Atoi(value)
		if err != nil {
			return errors.New("want a whole number")
		}
		sh.flags.retries = n
		if lc, ok := sh.conn.(*liveConn); ok {
			lc.setRetries(n)
		}
	case "net:reconnect-interval-base":
		secs, err := strconv.ParseFloat(value, 64)
		if err != nil || secs <= 0 {
			return errors.New("want a number of seconds greater than 0")
		}
		if lc, ok := sh.conn.(*liveConn); ok {
			lc.setBackoffBase(time.Duration(secs * float64(time.Second)))
		}
	case "net:timeout":
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return errors.New("want a duration like 30s")
		}
		sh.flags.timeout = d
	case "mirror:parallel":
		if n, err := strconv.Atoi(value); err != nil || n < 1 {
			return errors.New("want a whole number of at least 1")
		}
	case "cmd:fail-exit":
		switch strings.ToLower(value) {
		case "yes", "true", "on", "1":
			s.keepGoing = false
		case "no", "false", "off", "0":
			s.keepGoing = true
		default:
			return errors.New("want yes or no")
		}
	}
	return nil
}

func (s *scriptRun) cmdAlias(app App, args []string) error {
	switch len(args) {
	case 0:
		names := make([]string, 0, len(s.aliases))
		for n := range s.aliases {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			app.resultf("alias %s %s", n, strings.Join(s.aliases[n], " "))
		}
		return nil
	case 1:
		delete(s.aliases, args[0])
		return nil
	}
	s.aliases[args[0]] = append([]string(nil), args[1:]...)
	return nil
}

func (s *scriptRun) cmdSource(args []string) error {
	if len(args) != 1 {
		return usageError("source takes one FILE")
	}
	if s.depth >= 8 {
		return usageError("source nested too deeply")
	}
	f, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	s.depth++
	defer func() { s.depth-- }()
	return s.runReader(f, args[0])
}

// cmdOpen connects (or reconnects) the script to a server: `open PROFILE`,
// `open HOST` or `open --host H --user U ...`.
func (s *scriptRun) cmdOpen(app App, args []string) error {
	fset := app.newFlagSet("open")
	c := &connFlags{}
	c.register(fset)
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	if fset.NArg() > 1 {
		return usageError("open takes at most one PROFILE or HOST")
	}
	if name := fset.Arg(0); name != "" {
		c.profile, c.host = "", ""
		if cfg, err := app.loadConfig(); err == nil {
			for _, p := range cfg.Profiles {
				if p.Name == name {
					c.profile = name
				}
			}
		}
		if c.profile == "" {
			c.host = name
		}
	}
	sh := app.shared
	if c.host == "" && c.profile == "" {
		return usageError("open needs a PROFILE or HOST")
	}
	defaultRetries(fset, c, 3)
	if err := app.resolveLimit(c); err != nil {
		return err
	}
	target, err := app.target(c)
	if err != nil {
		return err
	}
	creds, err := app.credentials(target, c)
	if err != nil {
		return err
	}
	conn, err := app.connectLive(target, creds, c)
	if err != nil {
		return err
	}
	if sh.conn != nil {
		_ = sh.conn.Close()
	}
	sh.conn, sh.target, sh.creds, sh.flags, sh.cwd = conn, target, creds, c, target.Home()
	if c.limit != nil {
		sh.limit = c.limit
	}
	return nil
}

func (s *scriptRun) closeConn(sh *sharedSession) error {
	if sh.conn == nil {
		return nil
	}
	err := sh.conn.Close()
	sh.conn = nil
	return err
}
