package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"tideftp/internal/session"
)

// sharedSession is the single connection a script or shell runs over, plus
// the session state `cd` changes.
type sharedSession struct {
	conn   session.Conn
	target session.Target
	cwd    string
	quiet  bool
}

// keepOpen stops a command's deferred Close from ending the shared
// connection; the script runner closes it once, at the end.
type keepOpen struct{ session.Conn }

func (keepOpen) Close() error { return nil }

// splitCommands tokenizes one line of script into commands. Words are split
// on whitespace; single quotes keep everything literal; double quotes allow
// \" and \\ ; a backslash outside quotes escapes the next character. An
// unquoted ; ends a command and an unquoted # at the start of a word begins a
// comment that runs to the end of the line.
func splitCommands(line string) ([][]string, error) {
	var cmds [][]string
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
	endCmd := func() {
		flushWord()
		if len(cur) > 0 {
			cmds = append(cmds, cur)
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
			endCmd()
			return cmds, nil
		case ch == ';' || ch == '\n':
			endCmd()
		case ch == ' ' || ch == '\t' || ch == '\r':
			flushWord()
		default:
			inWord = true
			word.WriteRune(ch)
		}
	}
	endCmd()
	return cmds, nil
}

// scriptRun is one script/shell invocation.
type scriptRun struct {
	app         App
	sh          *sharedSession
	keepGoing   bool
	echo        bool
	interactive bool
	failCode    int // first non-zero exit code seen
}

// errScriptExit ends a script early; code is the exit code asked for.
type errScriptExit struct{ code int }

func (e errScriptExit) Error() string { return "" }

func (a App) cmdScript(args []string, interactive bool) error {
	name := "script"
	if interactive {
		name = "shell"
	}
	fset := a.newFlagSet(name)
	var inline string
	var keepGoing, echo bool
	if !interactive {
		fset.StringVar(&inline, "c", "", "run these commands (separated by ; or newlines) instead of reading a file")
	}
	fset.BoolVar(&keepGoing, "k", interactive, "keep going after a command fails (the exit code is the first failure's)")
	fset.BoolVar(&echo, "x", false, "print each command to stderr before running it")
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

	target, err := a.target(c)
	if err != nil {
		return err
	}
	creds, err := a.credentials(target, c)
	if err != nil {
		return err
	}
	conn, err := a.dial(target, creds, c)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	sh := &sharedSession{conn: conn, target: target, cwd: target.Home(), quiet: c.quiet}
	b := a
	b.shared = sh
	run := &scriptRun{app: b, sh: sh, keepGoing: keepGoing, echo: echo, interactive: interactive}
	return run.loop(src)
}

func (s *scriptRun) loop(src io.Reader) error {
	reader := bufio.NewReader(src)
	for lineNo := 1; ; lineNo++ {
		if s.interactive {
			_, _ = fmt.Fprintf(s.app.Stderr, "tideftp %s> ", s.sh.cwd)
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
		cmds, perr := splitCommands(line)
		if perr != nil {
			if stop := s.fail(fmt.Errorf("line %d: %w", lineNo, perr), exitUsage); stop {
				return s.result()
			}
			continue
		}
		for _, words := range cmds {
			if s.echo {
				s.app.statusf("+ %s", strings.Join(words, " "))
			}
			err := s.runOne(words[0], words[1:])
			var exit errScriptExit
			if errors.As(err, &exit) {
				if exit.code != 0 && s.failCode == 0 {
					s.failCode = exit.code
				}
				return s.result()
			}
			if err != nil {
				if stop := s.fail(err, 0); stop {
					return s.result()
				}
			}
		}
	}
	return s.result()
}

// fail reports a command's error and decides whether the script stops.
func (s *scriptRun) fail(err error, code int) (stop bool) {
	if code == 0 {
		code = s.app.report(err)
	} else {
		s.app.report(err)
	}
	if s.failCode == 0 {
		s.failCode = code
	}
	return !s.keepGoing
}

func (s *scriptRun) result() error {
	if s.failCode != 0 {
		return &codedError{code: s.failCode}
	}
	return nil
}

// runOne runs a builtin or a regular subcommand.
func (s *scriptRun) runOne(cmd string, args []string) error {
	switch cmd {
	case "exit", "quit":
		code := 0
		if len(args) > 0 {
			n, err := strconv.Atoi(args[0])
			if err != nil {
				return usageError("exit takes a number")
			}
			code = n
		}
		return errScriptExit{code: code}
	case "pwd":
		s.app.resultf("%s", s.sh.cwd)
		return nil
	case "lpwd":
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		s.app.resultf("%s", wd)
		return nil
	case "cd":
		if len(args) > 1 {
			return usageError("cd takes one directory")
		}
		dir := s.sh.target.Home()
		if len(args) == 1 {
			dir = resolveRemote(s.sh.cwd, args[0])
		}
		entry, err := s.sh.conn.FS().Stat(context.Background(), dir)
		if err != nil {
			return err
		}
		if !entry.IsDirLike() {
			return usageError("%s is not a directory", dir)
		}
		s.sh.cwd = dir
		return nil
	case "lcd":
		if len(args) != 1 {
			return usageError("lcd takes one directory")
		}
		return os.Chdir(args[0])
	case "sync", "mirror":
		// sync opens its own connections, so it cannot ride the shared one.
		// It still runs, but needs its own --profile / :PATH locations.
	case "script", "shell":
		return usageError("scripts cannot be nested")
	case "help":
		s.app.usage()
		return nil
	}
	known, err := s.app.dispatch(cmd, args)
	if !known {
		return usageError("unknown command %q", cmd)
	}
	return err
}
