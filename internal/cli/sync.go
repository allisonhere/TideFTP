package cli

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"tideftp/internal/domain"
	"tideftp/internal/localfs"
	"tideftp/internal/session"
	"tideftp/internal/transfer"
	"tideftp/internal/vfs"
)

// syncSkew is how much newer a source file's mtime must be before a same-size
// file counts as changed. It absorbs clock skew between client and server and
// filesystems that store mtimes coarsely. (The TUI's mirror uses the same
// value.)
const syncSkew = 2 * time.Second

// defaultTransfers is how many files move at once. Each worker holds its own
// connection, because a transfer engine serves one transfer at a time.
const defaultTransfers = 4

// syncSide is one end of a sync: the local disk, or a server.
type syncSide struct {
	remote bool
	fs     vfs.FS
	root   string

	target session.Target
	creds  session.Credentials
	flags  *connFlags
	conn   session.Conn // the connection used for planning; nil when local
	shared session.Conn // a script's own connection, when this side rides it
}

// newSide resolves a location into a root path and (for a server) the
// connection details. Nothing is dialled yet.
func (a App) newSide(loc location, c *connFlags) (*syncSide, error) {
	if !loc.remote {
		abs, err := filepath.Abs(loc.path)
		if err != nil {
			return nil, err
		}
		return &syncSide{fs: localfs.New(), root: abs}, nil
	}
	if sh := a.shared; sh != nil && loc.profile == "" {
		// Inside a script an unnamed remote is the script's own connection and
		// working directory; extra workers still dial their own from sh.target.
		if sh.conn == nil {
			return nil, usageError("not connected: use open HOST|PROFILE first")
		}
		t := sh.target
		t.StartPath = sh.cwd
		return &syncSide{remote: true, root: resolveRemote(sh.cwd, loc.path),
			target: t, creds: sh.creds, flags: sh.flags, shared: sh.conn}, nil
	}
	cf := *c
	if loc.profile != "" {
		// A named profile is complete in itself: the connection flags
		// (--host, --user, ...) describe the unnamed ":" location only.
		cf.profile = loc.profile
		cf.host, cf.user, cf.protocol, cf.path, cf.port = "", "", "", "", 0
	}
	target, err := a.target(&cf)
	if err != nil {
		return nil, err
	}
	creds, err := a.credentials(target, &cf)
	if err != nil {
		return nil, err
	}
	return &syncSide{
		remote: true,
		root:   resolveRemote(target.Home(), loc.path),
		target: target, creds: creds, flags: &cf,
	}, nil
}

func (a App) dialSide(s *syncSide) (session.Conn, error) {
	dial := func() (session.Conn, error) { return a.dial(s.target, s.creds, s.flags) }
	conn, err := dial()
	if err != nil {
		return nil, err
	}
	return a.live(conn, dial, s.flags), nil
}

// join resolves a slash-separated path relative to the side's root.
func (s *syncSide) join(rel string) string {
	if s.remote {
		return vfs.ChildRemote(s.root, rel)
	}
	return filepath.Join(s.root, filepath.FromSlash(rel))
}

func (s *syncSide) ensureDir(ctx context.Context, rel string) error {
	if s.remote {
		return ensureRemoteDir(ctx, s.fs, s.join(rel))
	}
	return os.MkdirAll(s.join(rel), 0o755)
}

type opKind int

const (
	opMkdir opKind = iota
	opCopy
	opUpdate
	opDelFile
	opDelDir
)

func (k opKind) String() string {
	return [...]string{"mkdir", "copy", "update", "delete", "delete"}[k]
}

type syncOp struct {
	kind opKind
	rel  string
	src  domain.Entry
}

// syncRun is one sync invocation's settings and state.
type syncRun struct {
	app      App
	src, dst *syncSide
	filter   syncFilter

	limit *transfer.Limiter

	dryRun, del, checksum, sizeOnly, resume, allowEmpty, quiet bool
	onlyNewer, onlyMissing, noEmptyDirs, verify, deleteFirst   bool
	dereference, noPerms, noRecursion, anyChange               bool
	transfers, maxErrors, pgetN                                int
	pgetMinStr                                                 string
	pgetMin                                                    int64
	onChange, logFile                                          string
	logw                                                       io.WriteCloser

	mu       sync.Mutex
	failures []error
	done     struct{ copied, updated, deleted, dirs int }
	bytes    int64
}

func (r *syncRun) fail(err error) {
	r.mu.Lock()
	r.failures = append(r.failures, err)
	r.mu.Unlock()
}

func (r *syncRun) announce(kind opKind, rel string) {
	r.mu.Lock()
	if kind != opMkdir || !r.dryRun {
		r.anyChange = true
	}
	if r.logw != nil {
		_, _ = fmt.Fprintf(r.logw, "%s %-6s %s\n", time.Now().Format(time.RFC3339), kind, rel)
	}
	r.mu.Unlock()
	if r.quiet {
		return
	}
	r.app.resultf("%-6s %s", kind, rel)
}

// syncFlagVals holds the flags that need parsing after flag.Parse.
type syncFlagVals struct {
	includes, excludes, includeRE, excludeRE               multiFlag
	minSize, maxSize, minAge, maxAge, newerThan, olderThan string
	reverse                                                bool
}

// registerFlags declares the options sync and mirror share. lftp selects the
// lftp spellings for mirror: -x/--exclude are regular expressions there (and
// -X/--exclude-glob the globs), -n is --only-newer, -P is --parallel, -e is
// --delete, -p is --no-perms and so on.
func (r *syncRun) registerFlags(fset *flag.FlagSet, v *syncFlagVals, lftp bool) {
	parallel := defaultTransfers
	if sh := r.app.shared; sh != nil {
		if raw, ok := sh.settings.get("mirror:parallel"); ok {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				parallel = n
			}
		}
	}
	both := func(p *bool, long, short, usage string) {
		fset.BoolVar(p, long, false, usage)
		if short != "" {
			fset.BoolVar(p, short, false, usage)
		}
	}
	both(&r.dryRun, "dry-run", "", "show what would change and change nothing")
	both(&r.del, "delete", "", "also delete destination files that are not in the source")
	both(&r.checksum, "checksum", "", "compare same-size files by SHA-256 instead of modification time")
	both(&r.sizeOnly, "size-only", "", "compare by size alone")
	both(&r.sizeOnly, "ignore-time", "", "same as --size-only")
	both(&r.resume, "resume", "c", "continue partial files left by an interrupted run (uploads and downloads)")
	both(&r.allowEmpty, "allow-empty-source", "", "let --delete run when the source is empty")
	both(&r.onlyMissing, "only-missing", "", "only copy files the destination lacks; never update")
	both(&r.noEmptyDirs, "no-empty-dirs", "", "do not create directories that would stay empty")
	both(&r.verify, "verify", "", "re-read each transferred file and compare SHA-256 with the source")
	both(&r.deleteFirst, "delete-first", "", "with --delete, delete before transferring")
	both(&r.noRecursion, "no-recursion", "", "do not descend into subdirectories")
	both(&r.noPerms, "no-perms", "", "do not copy permissions")
	fset.IntVar(&r.pgetN, "use-pget-n", 1, "download big files over this many connections each")
	fset.StringVar(&r.pgetMinStr, "pget-min-size", "8M", "with --use-pget-n, only files at least this big are split")
	fset.IntVar(&r.maxErrors, "max-errors", 0, "stop starting new transfers after this many failures (0 = never)")
	fset.StringVar(&r.onChange, "on-change", "", "run this shell command if anything was copied, updated or deleted")
	fset.StringVar(&r.logFile, "log", "", "append each action to this file")
	fset.StringVar(&v.minSize, "min-size", "", "skip files smaller than this (e.g. 10k, 5M)")
	fset.StringVar(&v.maxSize, "max-size", "", "skip files larger than this")
	fset.StringVar(&v.minAge, "min-age", "", "skip files modified more recently than this (e.g. 2h, 7d)")
	fset.StringVar(&v.maxAge, "max-age", "", "skip files modified longer ago than this")
	fset.StringVar(&v.newerThan, "newer-than", "", "only files modified after this date or this file's mtime")
	fset.StringVar(&v.olderThan, "older-than", "", "only files modified before this date or this file's mtime")
	if !lftp {
		both(&r.dryRun, "n", "", "same as --dry-run")
		both(&r.onlyNewer, "only-newer", "", "only update when the source is newer; never because of size")
		both(&r.dereference, "dereference", "", "follow symlinked directories")
		fset.IntVar(&r.transfers, "transfers", parallel, "files to move in parallel (one connection each)")
		fset.IntVar(&r.transfers, "parallel", parallel, "same as --transfers")
		fset.Var(&v.includes, "include", "only sync files matching this glob (repeatable)")
		fset.Var(&v.excludes, "exclude", "skip files or directories matching this glob (repeatable)")
		fset.Var(&v.includeRE, "include-regex", "only sync paths matching this regular expression (repeatable)")
		fset.Var(&v.excludeRE, "exclude-regex", "skip paths matching this regular expression (repeatable)")
		return
	}
	both(&r.dryRun, "just-print", "", "same as --dry-run")
	both(&r.onlyNewer, "only-newer", "n", "only update when the source is newer; never because of size")
	both(&r.dereference, "dereference", "L", "follow symlinked directories")
	both(&v.reverse, "reverse", "R", "upload: mirror a local directory to the server")
	both(&r.del, "e", "", "same as --delete")
	var verbose bool
	both(&verbose, "verbose", "v", "accepted for lftp compatibility; actions are always printed")
	fset.IntVar(&r.transfers, "parallel", parallel, "files to move in parallel")
	fset.IntVar(&r.transfers, "P", parallel, "same as --parallel")
	fset.Var(&v.excludeRE, "exclude", "skip paths matching this regular expression (repeatable)")
	fset.Var(&v.excludeRE, "x", "same as --exclude")
	fset.Var(&v.excludes, "exclude-glob", "skip files or directories matching this glob (repeatable)")
	fset.Var(&v.excludes, "X", "same as --exclude-glob")
	fset.Var(&v.includeRE, "include", "only mirror paths matching this regular expression (repeatable)")
	fset.Var(&v.includeRE, "i", "same as --include")
	fset.Var(&v.includes, "include-glob", "only mirror files matching this glob (repeatable)")
	fset.Var(&v.includes, "I", "same as --include-glob")
	both(&r.noPerms, "p", "", "same as --no-perms")
	// lftp's short form. Not offered on sync, where -r elsewhere (get, put, rm)
	// means "recursive" and sync is recursive already.
	fset.BoolVar(&r.noRecursion, "r", false, "same as --no-recursion")
}

// finish validates the parsed values and builds the filter.
func (r *syncRun) finish(v *syncFlagVals) error {
	if r.pgetN < 1 {
		return usageError("--use-pget-n must be at least 1")
	}
	var perr error
	if r.pgetMin, perr = parseSize(r.pgetMinStr); perr != nil {
		return usageError("--pget-min-size: %v", perr)
	}
	if r.transfers < 1 {
		return usageError("--transfers must be at least 1")
	}
	if r.checksum && r.sizeOnly {
		return usageError("--checksum and --size-only are mutually exclusive")
	}
	if r.onlyMissing && (r.onlyNewer || r.checksum) {
		return usageError("--only-missing cannot be combined with --only-newer or --checksum")
	}
	r.filter = syncFilter{includes: v.includes, excludes: v.excludes, now: time.Now()}
	for _, p := range append(append([]string{}, v.includes...), v.excludes...) {
		if _, err := path.Match(strings.TrimPrefix(p, "/"), ""); err != nil {
			return usageError("bad pattern %q: %v", p, err)
		}
	}
	for _, spec := range []struct {
		src  multiFlag
		dest *[]*regexp.Regexp
	}{{v.includeRE, &r.filter.includeRE}, {v.excludeRE, &r.filter.excludeRE}} {
		for _, p := range spec.src {
			re, err := regexp.Compile(p)
			if err != nil {
				return usageError("bad regular expression %q: %v", p, err)
			}
			*spec.dest = append(*spec.dest, re)
		}
	}
	var err error
	if r.filter.minSize, err = parseSize(v.minSize); err != nil {
		return usageError("--min-size: %v", err)
	}
	if r.filter.maxSize, err = parseSize(v.maxSize); err != nil {
		return usageError("--max-size: %v", err)
	}
	if r.filter.minAge, err = parseAge(v.minAge); err != nil {
		return usageError("--min-age: %v", err)
	}
	if r.filter.maxAge, err = parseAge(v.maxAge); err != nil {
		return usageError("--max-age: %v", err)
	}
	if r.filter.newerThan, err = parseWhen(v.newerThan); err != nil {
		return usageError("--newer-than: %v", err)
	}
	if r.filter.olderThan, err = parseWhen(v.olderThan); err != nil {
		return usageError("--older-than: %v", err)
	}
	return nil
}

func (a App) cmdSync(args []string) error {
	fset := a.newFlagSet("sync")
	r := &syncRun{app: a}
	v := &syncFlagVals{}
	r.registerFlags(fset, v, false)
	c := &connFlags{}
	c.register(fset)
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	if fset.NArg() != 2 {
		return usageError("sync needs SRC and DST")
	}
	return a.startSync(r, v, c, fset, parseLocation(fset.Arg(0)), parseLocation(fset.Arg(1)))
}

// cmdMirror is sync with lftp's argument shape and option names:
//
//	mirror [opts] [REMOTE [LOCAL]]      download (LOCAL defaults to REMOTE's name)
//	mirror -R [opts] [LOCAL [REMOTE]]   upload
//
// REMOTE defaults to the current remote directory and may be PROFILE:/path.
func (a App) cmdMirror(args []string) error {
	fset := a.newFlagSet("mirror")
	r := &syncRun{app: a}
	v := &syncFlagVals{}
	r.registerFlags(fset, v, true)
	c := &connFlags{}
	c.register(fset)
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	if fset.NArg() > 2 {
		return usageError("mirror takes at most two directories")
	}
	asRemote := func(op string) location {
		if loc := parseLocation(op); loc.remote {
			return loc
		}
		return location{remote: true, path: op}
	}
	first, second := fset.Arg(0), fset.Arg(1)
	var src, dst location
	if !v.reverse {
		src = asRemote(first)
		target := second
		if target == "" {
			target = path.Base(strings.TrimRight(src.path, "/"))
			if target == "." || target == "/" || target == "" {
				target = "."
			}
		}
		dst = location{path: target}
	} else {
		if first == "" {
			first = "."
		}
		src = location{path: first}
		target := second
		if target == "" {
			if abs, err := filepath.Abs(first); err == nil {
				target = filepath.Base(abs)
			}
		}
		dst = asRemote(target)
	}
	return a.startSync(r, v, c, fset, src, dst)
}

// startSync is the common tail of sync and mirror: validate, resolve both
// sides, and run.
func (a App) startSync(r *syncRun, v *syncFlagVals, c *connFlags, fset *flag.FlagSet, srcLoc, dstLoc location) error {
	r.quiet = c.quiet
	defaultRetries(fset, c, 3)
	if err := a.resolveLimit(c); err != nil {
		return err
	}
	r.limit = c.limit
	if err := r.finish(v); err != nil {
		return err
	}
	if !srcLoc.remote && !dstLoc.remote {
		return usageError("one side of a sync must be remote (PROFILE:/path or :/path)")
	}
	if c.passwordStdin && srcLoc.remote && dstLoc.remote {
		return usageError("--password-stdin can only supply one password; use a saved profile for the other side")
	}
	var err error
	if r.src, err = a.newSide(srcLoc, c); err != nil {
		return err
	}
	if r.dst, err = a.newSide(dstLoc, c); err != nil {
		return err
	}
	if r.logFile != "" {
		f, err := os.OpenFile(r.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		r.logw = f
		defer func() { _ = f.Close() }()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return r.run(ctx)
}

func (r *syncRun) run(ctx context.Context) error {
	for _, s := range []*syncSide{r.src, r.dst} {
		if !s.remote {
			continue
		}
		if s.shared != nil {
			s.conn, s.fs = s.shared, s.shared.FS()
			continue
		}
		conn, err := r.app.dialSide(s)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		s.conn, s.fs = conn, conn.FS()
	}

	srcAll, err := r.scanSource(ctx)
	if err != nil {
		return err
	}
	dstAll, dstRootExists, err := r.scanDest(ctx)
	if err != nil {
		return err
	}

	ops, unchanged, err := r.plan(ctx, srcAll, dstAll)
	if err != nil {
		return err
	}

	var files, dirsToCreate []syncOp
	var deletes []syncOp
	for _, op := range ops {
		switch op.kind {
		case opMkdir:
			dirsToCreate = append(dirsToCreate, op)
		case opCopy, opUpdate:
			files = append(files, op)
		default:
			deletes = append(deletes, op)
		}
	}

	if r.dryRun {
		for _, op := range ops {
			r.announce(op.kind, op.rel)
		}
		r.summary(unchanged, 0)
		return r.failureError()
	}

	if !dstRootExists {
		if err := r.dst.ensureDir(ctx, ""); err != nil {
			return fmt.Errorf("create %s: %w", r.dst.root, err)
		}
	}
	for _, op := range dirsToCreate {
		if err := r.dst.ensureDir(ctx, op.rel); err != nil {
			r.fail(fmt.Errorf("mkdir %s: %w", op.rel, err))
			continue
		}
		r.announce(op.kind, op.rel)
		r.mu.Lock()
		r.done.dirs++
		r.mu.Unlock()
	}
	// Deleting only after every copy succeeded means a failed or interrupted
	// run never removes a destination file whose replacement did not arrive.
	// --delete-first trades that guarantee for freeing space before copying.
	doDeletes := func() {
		if len(r.failures) == 0 && ctx.Err() == nil {
			for _, op := range deletes {
				if err := r.dst.fs.Remove(ctx, r.dst.join(op.rel)); err != nil {
					r.fail(fmt.Errorf("delete %s: %w", op.rel, err))
					continue
				}
				r.announce(op.kind, op.rel)
				r.done.deleted++
			}
		} else if len(deletes) > 0 {
			r.app.statusf("skipping %d deletion(s) because the sync did not complete cleanly", len(deletes))
		}
	}
	if r.deleteFirst {
		doDeletes()
	}
	r.transferAll(ctx, files)
	if !r.deleteFirst {
		doDeletes()
	}

	r.summary(unchanged, len(r.failures))
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.onChange != "" && r.anyChange && len(r.failures) == 0 {
		cmd := exec.CommandContext(ctx, "sh", "-c", r.onChange)
		cmd.Stdout, cmd.Stderr = lockedWriter{r.app.Stdout}, lockedWriter{r.app.Stderr}
		if err := cmd.Run(); err != nil {
			r.fail(fmt.Errorf("--on-change command: %w", err))
		}
	}
	return r.failureError()
}

func (r *syncRun) failureError() error {
	if len(r.failures) == 0 {
		return nil
	}
	for _, err := range r.failures {
		r.app.statusf("tideftp: %v", err)
	}
	return &codedError{code: exitFailure, err: fmt.Errorf("%d operation(s) failed", len(r.failures))}
}

func (r *syncRun) summary(unchanged, failed int) {
	if r.quiet {
		return
	}
	prefix := "sync"
	if r.dryRun {
		prefix = "sync (dry run)"
	}
	r.app.statusf("%s: %d new, %d updated, %d unchanged, %d deleted, %s transferred, %d failed",
		prefix, r.done.copied, r.done.updated, unchanged, r.done.deleted, humanBytes(r.bytes), failed)
}

func (r *syncRun) scanSource(ctx context.Context) (map[string]domain.Entry, error) {
	root, err := r.src.fs.Stat(ctx, r.src.root)
	if err != nil {
		return nil, fmt.Errorf("source %s: %w", r.src.root, err)
	}
	if !root.IsDirLike() {
		return nil, usageError("source %s is not a directory; sync works on directories", r.src.root)
	}
	return walkTree(ctx, r.src.fs, r.src.root, r.filter, r.dereference, r.noRecursion)
}

func (r *syncRun) scanDest(ctx context.Context) (map[string]domain.Entry, bool, error) {
	root, err := r.dst.fs.Stat(ctx, r.dst.root)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]domain.Entry{}, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("destination %s: %w", r.dst.root, err)
	}
	if !root.IsDirLike() {
		return nil, false, usageError("destination %s is not a directory", r.dst.root)
	}
	all, err := walkTree(ctx, r.dst.fs, r.dst.root, r.filter, r.dereference, r.noRecursion)
	return all, true, err
}

// maxWalkDepth bounds a walk that follows symlinks, so a link cycle ends.
const maxWalkDepth = 40

// walkTree lists every file and directory under root, keyed by slash-separated
// path relative to it. Symlinked directories are not followed (a link could
// leave the tree or loop); a directory an --exclude names is not entered.
func walkTree(ctx context.Context, fsys vfs.FS, root string, f syncFilter, deref, noRecurse bool) (map[string]domain.Entry, error) {
	out := map[string]domain.Entry{}
	type item struct {
		dir, rel string
		depth    int
	}
	stack := []item{{root, "", 0}}
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := fsys.List(ctx, it.dir, true)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", it.dir, err)
		}
		for _, e := range entries {
			if e.Name == "." || e.Name == ".." {
				continue
			}
			rel := path.Join(it.rel, e.Name)
			switch {
			case e.IsDir() || (deref && e.LinksToDir):
				if noRecurse || f.dirExcluded(rel) || it.depth >= maxWalkDepth {
					continue
				}
				// A followed link is recorded as the directory it points at.
				e.Kind = domain.EntryDir
				out[rel] = e
				stack = append(stack, item{fsys.Child(it.dir, e.Name), rel, it.depth + 1})
			case e.LinksToDir:
			default:
				out[rel] = e
			}
		}
	}
	return out, nil
}

// plan compares the trees and returns the operations to run, in a safe order
// (directories shallow-first, then files, then deletions deepest-first).
func (r *syncRun) plan(ctx context.Context, srcAll, dstAll map[string]domain.Entry) ([]syncOp, int, error) {
	rels := make([]string, 0, len(srcAll))
	for rel := range srcAll {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	var files, deletes []syncOp
	needDirs := map[string]bool{}
	unchanged := 0

	for _, rel := range rels {
		s := srcAll[rel]
		d, inDst := dstAll[rel]
		if s.IsDir() {
			if inDst && !d.IsDir() {
				r.fail(fmt.Errorf("%s: a directory in the source but a file at the destination", rel))
			} else if !inDst && !r.noEmptyDirs {
				needDirs[rel] = true
			}
			continue
		}
		if !r.filter.selects(rel, s) {
			continue
		}
		switch {
		case !inDst:
			files = append(files, syncOp{kind: opCopy, rel: rel, src: s})
			needDirs[path.Dir(rel)] = true
		case d.IsDir():
			r.fail(fmt.Errorf("%s: a file in the source but a directory at the destination", rel))
		case r.onlyMissing:
			unchanged++
		default:
			differs, err := r.differs(ctx, rel, s, d)
			if err != nil {
				return nil, 0, err
			}
			if differs {
				files = append(files, syncOp{kind: opUpdate, rel: rel, src: s})
			} else {
				unchanged++
			}
		}
	}

	if r.del {
		if len(srcAll) == 0 && len(dstAll) > 0 && !r.allowEmpty {
			return nil, 0, fmt.Errorf("refusing to --delete: the source is empty and the destination is not (pass --allow-empty-source if that is intended)")
		}
		kept := map[string]bool{}
		var dirs []string
		for rel, d := range dstAll {
			if _, inSrc := srcAll[rel]; inSrc {
				// Present on both sides: never a deletion, and it keeps its
				// parent directories alive.
				kept[path.Dir(rel)] = true
				continue
			}
			if d.IsDir() {
				dirs = append(dirs, rel)
				continue
			}
			if !r.filter.namePasses(rel) {
				kept[path.Dir(rel)] = true
				continue
			}
			deletes = append(deletes, syncOp{kind: opDelFile, rel: rel})
		}
		sort.Slice(deletes, func(i, j int) bool { return deletes[i].rel < deletes[j].rel })
		// Deepest first, so a directory is judged after everything inside it.
		sort.Slice(dirs, func(i, j int) bool {
			di, dj := strings.Count(dirs[i], "/"), strings.Count(dirs[j], "/")
			if di != dj {
				return di > dj
			}
			return dirs[i] < dirs[j]
		})
		for _, rel := range dirs {
			if kept[rel] {
				kept[path.Dir(rel)] = true
				continue
			}
			deletes = append(deletes, syncOp{kind: opDelDir, rel: rel})
		}
	}

	// Directories, with their ancestors, that must exist and do not yet.
	var mk []string
	seen := map[string]bool{}
	for dir := range needDirs {
		for d := dir; d != "." && d != "" && d != "/"; d = path.Dir(d) {
			if seen[d] {
				break
			}
			seen[d] = true
			if _, ok := dstAll[d]; !ok {
				mk = append(mk, d)
			}
		}
	}
	sort.Slice(mk, func(i, j int) bool {
		di, dj := strings.Count(mk[i], "/"), strings.Count(mk[j], "/")
		if di != dj {
			return di < dj
		}
		return mk[i] < mk[j]
	})
	ops := make([]syncOp, 0, len(mk)+len(files)+len(deletes))
	for _, d := range mk {
		ops = append(ops, syncOp{kind: opMkdir, rel: d})
	}
	ops = append(ops, files...)
	ops = append(ops, deletes...)
	return ops, unchanged, nil
}

// differs decides whether an existing destination file needs replacing.
func (r *syncRun) differs(ctx context.Context, rel string, s, d domain.Entry) (bool, error) {
	if r.onlyNewer {
		return !s.Modified.IsZero() && !d.Modified.IsZero() && s.Modified.After(d.Modified.Add(syncSkew)), nil
	}
	if s.Size != d.Size {
		return true, nil
	}
	switch {
	case r.sizeOnly:
		return false, nil
	case r.checksum:
		a, err := hashFile(ctx, r.src.fs, r.src.join(rel))
		if err != nil {
			return false, fmt.Errorf("checksum %s: %w", rel, err)
		}
		b, err := hashFile(ctx, r.dst.fs, r.dst.join(rel))
		if err != nil {
			return false, fmt.Errorf("checksum %s: %w", rel, err)
		}
		return a != b, nil
	default:
		return !s.Modified.IsZero() && !d.Modified.IsZero() && s.Modified.After(d.Modified.Add(syncSkew)), nil
	}
}

func hashFile(ctx context.Context, fsys vfs.FS, p string) (string, error) {
	rc, err := fsys.Open(ctx, p)
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, rc); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// syncWorker owns the connections one goroutine transfers over.
type syncWorker struct {
	src, dst session.Conn
	tmpDir   string
}

// transferAll moves every file with up to r.transfers workers. The first
// worker reuses the planning connections; the rest dial their own, and a
// worker that cannot connect simply stops taking work — the others finish it.
func (r *syncRun) transferAll(ctx context.Context, files []syncOp) {
	if len(files) == 0 {
		return
	}
	n := min(r.transfers, len(files))
	jobs := make(chan syncOp)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := &syncWorker{}
			if i == 0 {
				w.src, w.dst = r.src.conn, r.dst.conn
			} else {
				if err := r.dialWorker(w); err != nil {
					r.app.statusf("worker %d could not connect (%v); continuing with fewer", i+1, err)
					return
				}
				defer func() {
					for _, c := range []session.Conn{w.src, w.dst} {
						if c != nil {
							_ = c.Close()
						}
					}
				}()
			}
			if r.src.remote && r.dst.remote {
				dir, err := os.MkdirTemp("", "tideftp-sync-")
				if err != nil {
					r.fail(err)
					return
				}
				w.tmpDir = dir
				defer func() { _ = os.RemoveAll(dir) }()
			}
			for op := range jobs {
				if ctx.Err() != nil || r.tooManyErrors() {
					continue
				}
				err := r.copyOne(ctx, w, op)
				if err == nil && r.verify {
					err = r.verifyOne(ctx, w, op)
				}
				if err != nil {
					r.fail(fmt.Errorf("%s: %w", op.rel, err))
					continue
				}
				r.applyMeta(ctx, w, op)
				r.announce(op.kind, op.rel)
				r.mu.Lock()
				if op.kind == opCopy {
					r.done.copied++
				} else {
					r.done.updated++
				}
				r.bytes += op.src.Size
				r.mu.Unlock()
			}
		}(i)
	}
	for _, op := range files {
		jobs <- op
	}
	close(jobs)
	wg.Wait()
}

func (r *syncRun) tooManyErrors() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxErrors > 0 && len(r.failures) >= r.maxErrors
}

// sideFS is the filesystem a worker uses for one side: its own connection's
// when the side is remote, the local disk otherwise.
func (r *syncRun) sideFS(side *syncSide, conn session.Conn) vfs.FS {
	if conn != nil {
		return conn.FS()
	}
	return side.fs
}

// verifyOne re-reads both ends of a transferred file and compares hashes.
func (r *syncRun) verifyOne(ctx context.Context, w *syncWorker, op syncOp) error {
	want, err := hashFile(ctx, r.sideFS(r.src, w.src), r.src.join(op.rel))
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	got, err := hashFile(ctx, r.sideFS(r.dst, w.dst), r.dst.join(op.rel))
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if want != got {
		return fmt.Errorf("verify: %s differs from the source after transfer", op.rel)
	}
	return nil
}

// applyMeta carries the source's modification time and permissions onto an
// uploaded file, best effort: a server that cannot (FTP without MFMT, or any
// FTP for chmod) is skipped, never failed. Downloads are stamped when written.
func (r *syncRun) applyMeta(ctx context.Context, w *syncWorker, op syncOp) {
	if !r.dst.remote {
		if !r.noPerms {
			if perm, ok := currentPerm(op.src.Mode); ok {
				_ = os.Chmod(r.dst.join(op.rel), perm)
			}
		}
		return
	}
	dstFS, dstPath := r.sideFS(r.dst, w.dst), r.dst.join(op.rel)
	if !op.src.Modified.IsZero() {
		if setter, ok := dstFS.(vfs.MtimeSetter); ok {
			_ = setter.SetMtime(ctx, dstPath, op.src.Modified)
		}
	}
	if !r.noPerms {
		if perm, ok := currentPerm(op.src.Mode); ok {
			_ = dstFS.Chmod(ctx, dstPath, perm)
		}
	}
}

func (r *syncRun) dialWorker(w *syncWorker) error {
	var err error
	if r.src.remote {
		if w.src, err = r.app.dialSide(r.src); err != nil {
			return err
		}
	}
	if r.dst.remote {
		if w.dst, err = r.app.dialSide(r.dst); err != nil {
			if w.src != nil {
				_ = w.src.Close()
			}
			return err
		}
	}
	return nil
}

func (r *syncRun) copyOne(ctx context.Context, w *syncWorker, op syncOp) error {
	srcPath, dstPath := r.src.join(op.rel), r.dst.join(op.rel)
	switch {
	case !r.src.remote:
		return uploadAtomic(ctx, w.dst, srcPath, dstPath, op.src.Size, r.limit, r.resume)
	case !r.dst.remote:
		if r.pgetN > 1 && op.src.Size >= r.pgetMin {
			more := func() (session.Conn, error) { return r.app.dialSide(r.src) }
			return segmentedDownload(ctx, w.src, more, srcPath, dstPath, op.src, r.pgetN, r.limit)
		}
		return downloadAtomic(ctx, w.src.Engine(), srcPath, dstPath, op.src, r.resume, r.limit)
	default:
		tmp := filepath.Join(w.tmpDir, "relay")
		if err := downloadAtomic(ctx, w.src.Engine(), srcPath, tmp, op.src, false, r.limit); err != nil {
			return err
		}
		defer func() { _ = os.Remove(tmp) }()
		return uploadAtomic(ctx, w.dst, tmp, dstPath, op.src.Size, r.limit, false)
	}
}

// uploadAtomic sends local to remote as NAME.part and renames it into place,
// replacing any existing file only once the new one has fully arrived.
func uploadAtomic(ctx context.Context, conn session.Conn, local, remote string, size int64, limit *transfer.Limiter, resume bool) error {
	fsys := conn.FS()
	part := remote + ".part"
	var offset int64
	if resume {
		if st, err := fsys.Stat(ctx, part); err == nil && !st.IsDir() && st.Size < size {
			offset = st.Size
		}
	}
	req := transfer.Request{Direction: domain.Upload, Source: local, Destination: part, Size: size, Offset: offset, Limit: limit}
	if err := transfer.Copy(ctx, conn.Engine(), req, nil); err != nil {
		if !resume {
			_ = fsys.Remove(context.Background(), part)
		}
		return fmt.Errorf("upload: %w", err)
	}
	if _, err := fsys.Stat(ctx, remote); err == nil {
		if err := fsys.Remove(ctx, remote); err != nil {
			return fmt.Errorf("replace: %w", err)
		}
	}
	if err := fsys.Rename(ctx, part, remote); err != nil {
		return fmt.Errorf("finish: %w", err)
	}
	return nil
}

// downloadAtomic fetches remote into NAME.part, renames it into place and
// stamps it with the source's mtime, so the next run sees the files as equal.
// With resume, a leftover partial file shorter than the source is continued;
// without it a leftover is discarded, because the source may have changed.
func downloadAtomic(ctx context.Context, eng transfer.Engine, remote, local string, src domain.Entry, resume bool, limit *transfer.Limiter) error {
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return err
	}
	part := local + ".part"
	var offset int64
	if resume {
		if info, err := os.Stat(part); err == nil && info.Size() < src.Size {
			offset = info.Size()
		}
	}
	if offset == 0 {
		_ = os.Remove(part)
	}
	req := transfer.Request{Direction: domain.Download, Source: remote, Destination: part, Size: src.Size, Offset: offset, Limit: limit}
	if err := transfer.Copy(ctx, eng, req, nil); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	_ = os.Remove(local)
	if err := os.Rename(part, local); err != nil {
		return err
	}
	if !src.Modified.IsZero() {
		_ = os.Chtimes(local, src.Modified, src.Modified)
	}
	return nil
}
