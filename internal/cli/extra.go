package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"tideftp/internal/domain"
	"tideftp/internal/session"
	"tideftp/internal/vfs"
)

// Exit codes. They are part of the CLI's contract: a script can tell "the
// server is down" from "the password is wrong" from "that path is not there"
// without parsing stderr.
const (
	exitOK       = 0
	exitFailure  = 1 // an operation failed
	exitUsage    = 2 // bad flags or operands
	exitConnect  = 3 // could not reach or handshake with the server
	exitAuth     = 4 // the server rejected the credentials
	exitNotFound = 5 // a named remote path does not exist
)

// codedError carries an explicit exit code. A nil err makes it silent, which
// is what `exists` wants: the exit code is the whole answer.
type codedError struct {
	code int
	err  error
}

func (e *codedError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *codedError) Unwrap() error { return e.err }

// classifyDial wraps a connect failure with the exit code that describes it.
// A host-key refusal is returned untouched: Run reports it with its own hint.
func classifyDial(err error) error {
	if err == nil {
		return nil
	}
	var untrusted *session.UntrustedHostKeyError
	if errors.As(err, &untrusted) {
		return err
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"unable to authenticate", "authentication failed", "no supported methods remain",
		"login incorrect", "permission denied", "530",
	} {
		if strings.Contains(msg, marker) {
			return &codedError{code: exitAuth, err: err}
		}
	}
	return &codedError{code: exitConnect, err: err}
}

// exitCode maps an error from a subcommand to the process exit code.
func exitCode(err error) int {
	var coded *codedError
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, errUsage):
		return exitUsage
	case errors.As(err, &coded):
		return coded.code
	case errors.Is(err, fs.ErrNotExist):
		return exitNotFound
	default:
		return exitFailure
	}
}

// hasGlob reports whether s uses glob metacharacters.
func hasGlob(s string) bool { return strings.ContainsAny(s, "*?[") }

// expandRemote turns operand into the remote paths it names. A plain operand
// is returned as one resolved path without touching the server. A glob may
// appear in the last path component only (`/logs/*.gz`); like a shell, it does
// not match dotfiles unless the pattern itself starts with a dot. A glob that
// matches nothing is a not-found error rather than a silent no-op, so a typo
// cannot make a backup job "succeed" having copied nothing.
func expandRemote(ctx context.Context, fsys vfs.FS, base, operand string) ([]string, error) {
	resolved := resolveRemote(base, operand)
	if !hasGlob(operand) {
		return []string{resolved}, nil
	}
	dir, pattern := path.Split(resolved)
	dir = vfs.CleanRemote(dir)
	if hasGlob(dir) {
		return nil, usageError("%s: wildcards are only supported in the last path component", operand)
	}
	if _, err := path.Match(pattern, ""); err != nil {
		return nil, usageError("%s: bad pattern: %v", operand, err)
	}
	entries, err := fsys.List(ctx, dir, true)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name, ".") && !strings.HasPrefix(pattern, ".") {
			continue
		}
		if ok, _ := path.Match(pattern, e.Name); ok {
			out = append(out, fsys.Child(dir, e.Name))
		}
	}
	if len(out) == 0 {
		return nil, &codedError{code: exitNotFound, err: errors.New("no remote path matches " + operand)}
	}
	sort.Strings(out)
	return out, nil
}

// expandRemoteAll expands every operand, keeping order.
func expandRemoteAll(ctx context.Context, fsys vfs.FS, base string, operands []string) ([]string, error) {
	var out []string
	for _, op := range operands {
		paths, err := expandRemote(ctx, fsys, base, op)
		if err != nil {
			return nil, err
		}
		out = append(out, paths...)
	}
	return out, nil
}

// expandLocal expands an operand that names local files. A path that exists
// is taken literally even if it contains glob characters; otherwise a glob is
// expanded, which covers a quoted pattern the shell did not touch.
func expandLocal(operand string) ([]string, error) {
	if !hasGlob(operand) {
		return []string{operand}, nil
	}
	if _, err := os.Lstat(operand); err == nil {
		return []string{operand}, nil
	}
	matches, err := filepath.Glob(operand)
	if err != nil {
		return nil, usageError("%s: bad pattern: %v", operand, err)
	}
	if len(matches) == 0 {
		return nil, &codedError{code: exitNotFound, err: errors.New("no local path matches " + operand)}
	}
	sort.Strings(matches)
	return matches, nil
}

// entryJSON is the stable machine-readable shape of a listing entry.
type entryJSON struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"` // file, dir, symlink
	Size     int64  `json:"size"`
	Mode     string `json:"mode"`
	Modified string `json:"modified,omitempty"` // RFC 3339, UTC
}

func toEntryJSON(p string, e domain.Entry) entryJSON {
	kind := "file"
	switch e.Kind {
	case domain.EntryDir:
		kind = "dir"
	case domain.EntrySymlink:
		kind = "symlink"
	}
	out := entryJSON{Name: e.Name, Path: p, Type: kind, Size: e.Size, Mode: e.Mode}
	if out.Name == "" {
		out.Name = path.Base(p)
	}
	if !e.Modified.IsZero() {
		out.Modified = e.Modified.UTC().Format(time.RFC3339)
	}
	return out
}

func (a App) printJSON(v any) error {
	enc := json.NewEncoder(a.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (a App) cmdStat(args []string) error {
	fset := a.newFlagSet("stat")
	var asJSON bool
	fset.BoolVar(&asJSON, "json", false, "print a JSON array")
	c := &connFlags{}
	c.register(fset)
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	if fset.NArg() == 0 {
		return usageError("stat needs at least one PATH")
	}
	conn, target, err := a.open(c)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	ctx := context.Background()
	base := target.Home()
	paths, err := expandRemoteAll(ctx, conn.FS(), base, fset.Args())
	if err != nil {
		return err
	}
	var all []entryJSON
	for _, p := range paths {
		entry, err := conn.FS().Stat(ctx, p)
		if err != nil {
			return err
		}
		all = append(all, toEntryJSON(p, entry))
	}
	if asJSON {
		return a.printJSON(all)
	}
	if c.quiet {
		return nil
	}
	for _, e := range all {
		a.resultf("%s %s %d %s %s", e.Type, e.Mode, e.Size, e.Modified, e.Path)
	}
	return nil
}

// cmdExists is the building block for `if tideftp exists ...; then`. It prints
// nothing: exit 0 means every PATH exists (and matches -f / -d), exit 5 means
// at least one does not.
func (a App) cmdExists(args []string) error {
	fset := a.newFlagSet("exists")
	var wantFile, wantDir bool
	fset.BoolVar(&wantFile, "f", false, "require a regular file")
	fset.BoolVar(&wantDir, "d", false, "require a directory")
	c := &connFlags{}
	c.register(fset)
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	if fset.NArg() == 0 {
		return usageError("exists needs at least one PATH")
	}
	if wantFile && wantDir {
		return usageError("-f and -d are mutually exclusive")
	}
	conn, target, err := a.open(c)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	ctx := context.Background()
	base := target.Home()
	for _, operand := range fset.Args() {
		paths, err := expandRemote(ctx, conn.FS(), base, operand)
		var coded *codedError
		if errors.As(err, &coded) && coded.code == exitNotFound {
			return &codedError{code: exitNotFound}
		}
		if err != nil {
			return err
		}
		for _, p := range paths {
			entry, err := conn.FS().Stat(ctx, p)
			if errors.Is(err, fs.ErrNotExist) {
				return &codedError{code: exitNotFound}
			}
			if err != nil {
				return err
			}
			if (wantDir && !entry.IsDirLike()) || (wantFile && entry.IsDirLike()) {
				return &codedError{code: exitNotFound}
			}
		}
	}
	return nil
}

// readPasswordStdin reads one line from r, trimming the line ending.
func readPasswordStdin(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && line == "" {
		return "", usageError("--password-stdin: no password on standard input")
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", usageError("--password-stdin: empty password")
	}
	return line, nil
}
