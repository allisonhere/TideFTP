package cli

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"sort"
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
	return a.dial(s.target, s.creds, s.flags)
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

	dryRun, del, checksum, sizeOnly, resume, allowEmpty, quiet bool
	transfers                                                  int

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
	if r.quiet {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.app.resultf("%-6s %s", kind, rel)
}

func (a App) cmdSync(args []string) error {
	fset := a.newFlagSet("sync")
	r := &syncRun{app: a}
	var includes, excludes multiFlag
	var minSize, maxSize, minAge, maxAge string
	fset.BoolVar(&r.dryRun, "dry-run", false, "show what would change and change nothing")
	fset.BoolVar(&r.dryRun, "n", false, "same as --dry-run")
	fset.BoolVar(&r.del, "delete", false, "also delete destination files that are not in the source")
	fset.BoolVar(&r.checksum, "checksum", false, "compare same-size files by SHA-256 instead of modification time")
	fset.BoolVar(&r.sizeOnly, "size-only", false, "compare by size alone")
	fset.BoolVar(&r.resume, "resume", false, "continue a partial download left by an interrupted run")
	fset.BoolVar(&r.allowEmpty, "allow-empty-source", false, "let --delete run when the source is empty")
	fset.IntVar(&r.transfers, "transfers", defaultTransfers, "files to move in parallel (one connection each)")
	fset.Var(&includes, "include", "only sync files matching this glob (repeatable)")
	fset.Var(&excludes, "exclude", "skip files or directories matching this glob (repeatable)")
	fset.StringVar(&minSize, "min-size", "", "skip files smaller than this (e.g. 10k, 5M)")
	fset.StringVar(&maxSize, "max-size", "", "skip files larger than this")
	fset.StringVar(&minAge, "min-age", "", "skip files modified more recently than this (e.g. 2h, 7d)")
	fset.StringVar(&maxAge, "max-age", "", "skip files modified longer ago than this")
	c := &connFlags{}
	c.register(fset)
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	if fset.NArg() != 2 {
		return usageError("sync needs SRC and DST")
	}
	r.quiet = c.quiet
	if r.transfers < 1 {
		return usageError("--transfers must be at least 1")
	}
	if r.checksum && r.sizeOnly {
		return usageError("--checksum and --size-only are mutually exclusive")
	}

	r.filter = syncFilter{includes: includes, excludes: excludes, now: time.Now()}
	for _, p := range append(append([]string{}, includes...), excludes...) {
		if _, err := path.Match(strings.TrimPrefix(p, "/"), ""); err != nil {
			return usageError("bad pattern %q: %v", p, err)
		}
	}
	var err error
	if r.filter.minSize, err = parseSize(minSize); err != nil {
		return usageError("--min-size: %v", err)
	}
	if r.filter.maxSize, err = parseSize(maxSize); err != nil {
		return usageError("--max-size: %v", err)
	}
	if r.filter.minAge, err = parseAge(minAge); err != nil {
		return usageError("--min-age: %v", err)
	}
	if r.filter.maxAge, err = parseAge(maxAge); err != nil {
		return usageError("--max-age: %v", err)
	}

	srcLoc, dstLoc := parseLocation(fset.Arg(0)), parseLocation(fset.Arg(1))
	if !srcLoc.remote && !dstLoc.remote {
		return usageError("one side of a sync must be remote (PROFILE:/path or :/path)")
	}
	if c.passwordStdin && srcLoc.remote && dstLoc.remote {
		return usageError("--password-stdin can only supply one password; use a saved profile for the other side")
	}
	if r.src, err = a.newSide(srcLoc, c); err != nil {
		return err
	}
	if r.dst, err = a.newSide(dstLoc, c); err != nil {
		return err
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
	r.transferAll(ctx, files)

	// Deleting only after every copy succeeded means a failed or interrupted
	// run never removes a destination file whose replacement did not arrive.
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

	r.summary(unchanged, len(r.failures))
	if err := ctx.Err(); err != nil {
		return err
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
	return walkTree(ctx, r.src.fs, r.src.root, r.filter)
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
	all, err := walkTree(ctx, r.dst.fs, r.dst.root, r.filter)
	return all, true, err
}

// walkTree lists every file and directory under root, keyed by slash-separated
// path relative to it. Symlinked directories are not followed (a link could
// leave the tree or loop); a directory an --exclude names is not entered.
func walkTree(ctx context.Context, fsys vfs.FS, root string, f syncFilter) (map[string]domain.Entry, error) {
	out := map[string]domain.Entry{}
	type item struct{ dir, rel string }
	stack := []item{{root, ""}}
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
			case e.IsDir():
				if f.dirExcluded(rel) {
					continue
				}
				out[rel] = e
				stack = append(stack, item{fsys.Child(it.dir, e.Name), rel})
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
			} else if !inDst && !r.filter.active() {
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
				if ctx.Err() != nil {
					continue
				}
				if err := r.copyOne(ctx, w, op); err != nil {
					r.fail(fmt.Errorf("%s: %w", op.rel, err))
					continue
				}
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
		return uploadAtomic(ctx, w.dst, srcPath, dstPath, op.src.Size)
	case !r.dst.remote:
		return downloadAtomic(ctx, w.src.Engine(), srcPath, dstPath, op.src, r.resume)
	default:
		tmp := filepath.Join(w.tmpDir, "relay")
		if err := downloadAtomic(ctx, w.src.Engine(), srcPath, tmp, op.src, false); err != nil {
			return err
		}
		defer func() { _ = os.Remove(tmp) }()
		return uploadAtomic(ctx, w.dst, tmp, dstPath, op.src.Size)
	}
}

// uploadAtomic sends local to remote as NAME.part and renames it into place,
// replacing any existing file only once the new one has fully arrived.
func uploadAtomic(ctx context.Context, conn session.Conn, local, remote string, size int64) error {
	fsys := conn.FS()
	part := remote + ".part"
	req := transfer.Request{Direction: domain.Upload, Source: local, Destination: part, Size: size}
	if err := transfer.Copy(ctx, conn.Engine(), req, nil); err != nil {
		_ = fsys.Remove(context.Background(), part)
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
func downloadAtomic(ctx context.Context, eng transfer.Engine, remote, local string, src domain.Entry, resume bool) error {
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
	req := transfer.Request{Direction: domain.Download, Source: remote, Destination: part, Size: src.Size, Offset: offset}
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
