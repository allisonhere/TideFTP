package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"tideftp/internal/domain"
	"tideftp/internal/session"
	"tideftp/internal/transfer"
	"tideftp/internal/vfs"
)

// open resolves the connection flags, dials, and returns the live connection
// with the target it connected to. The caller closes the connection.
func (a App) open(c *connFlags) (session.Conn, session.Target, error) {
	conn, target, _, err := a.openWith(c)
	return conn, target, err
}

// openWith is open plus the credentials it used, for commands (pget, sync)
// that dial extra connections of their own to the same server.
func (a App) openWith(c *connFlags) (session.Conn, session.Target, session.Credentials, error) {
	if err := a.resolveLimit(c); err != nil {
		return nil, session.Target{}, session.Credentials{}, err
	}
	if sh := a.shared; sh != nil {
		// Inside a script every command rides the one open connection; its own
		// connection flags are ignored, and a leading `cd` is the base path.
		if sh.conn == nil {
			return nil, session.Target{}, session.Credentials{}, usageError("not connected: use open HOST|PROFILE first")
		}
		c.quiet = c.quiet || sh.quiet
		c.retries = sh.flags.retries
		target := sh.target
		target.StartPath = sh.cwd
		return keepOpen{sh.conn}, target, sh.creds, nil
	}
	target, err := a.target(c)
	if err != nil {
		return nil, session.Target{}, session.Credentials{}, err
	}
	creds, err := a.credentials(target, c)
	if err != nil {
		return nil, session.Target{}, session.Credentials{}, err
	}
	dial := func() (session.Conn, error) { return a.dial(target, creds, c) }
	conn, err := dial()
	if err != nil {
		return nil, session.Target{}, session.Credentials{}, err
	}
	return a.live(conn, dial, c), target, creds, nil
}

// resolveRemote joins a remote operand onto the connection's base directory.
// An absolute operand is used as-is; anything else is relative to base, which
// is --path or the profile's start_path (itself / by default). This is how a
// saved profile makes `tideftp get logs/app.log` mean the obvious thing.
func resolveRemote(base, operand string) string {
	switch {
	case operand == "":
		return vfs.CleanRemote(base)
	case strings.HasPrefix(operand, "/"):
		return vfs.CleanRemote(operand)
	default:
		return vfs.ChildRemote(base, operand)
	}
}

func (a App) cmdLs(args []string) error {
	fs := a.newFlagSet("ls")
	var long, asJSON bool
	fs.BoolVar(&long, "l", false, "long listing: mode, size, date, name")
	fs.BoolVar(&asJSON, "json", false, "print a JSON array of entries (name, path, type, size, mode, modified)")
	c := &connFlags{}
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	conn, target, err := a.open(c)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	ctx := context.Background()
	base := target.Home()
	operands := fs.Args()
	if len(operands) == 0 {
		operands = []string{base}
	}
	paths, err := expandRemoteAll(ctx, conn.FS(), base, operands)
	if err != nil {
		return err
	}
	rows := []entryJSON{}
	for _, remote := range paths {
		entry, err := conn.FS().Stat(ctx, remote)
		if err != nil {
			return err
		}
		if !entry.IsDirLike() {
			rows = append(rows, toEntryJSON(remote, entry))
			continue
		}
		entries, err := conn.FS().List(ctx, remote, true)
		if err != nil {
			return err
		}
		for _, e := range entries {
			rows = append(rows, toEntryJSON(conn.FS().Child(remote, e.Name), e))
		}
	}
	if asJSON {
		return a.printJSON(rows)
	}
	if c.quiet {
		return nil
	}
	for _, row := range rows {
		a.printRow(row, long)
	}
	return nil
}

func (a App) printRow(e entryJSON, long bool) {
	if !long {
		a.resultf("%s", e.Name)
		return
	}
	name := e.Name
	if e.Type == "dir" {
		name += "/"
	}
	modified := ""
	if e.Modified != "" {
		if t, err := time.Parse(time.RFC3339, e.Modified); err == nil {
			modified = t.Local().Format("2006-01-02 15:04")
		}
	}
	a.resultf("%s %10d  %s  %s", e.Mode, e.Size, modified, name)
}

func (a App) cmdGet(args []string) error {
	fs := a.newFlagSet("get")
	var recursive, force, resume bool
	fs.BoolVar(&recursive, "r", false, "download directories recursively")
	fs.BoolVar(&recursive, "recursive", false, "download directories recursively")
	fs.BoolVar(&force, "f", false, "overwrite an existing destination")
	fs.BoolVar(&force, "force", false, "overwrite an existing destination")
	fs.BoolVar(&resume, "resume", false, "continue a partially downloaded file")
	fs.BoolVar(&resume, "c", false, "same as --resume")
	var outDir string
	fs.StringVar(&outDir, "O", "", "download into this local directory (created if missing)")
	c := &connFlags{}
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	rest := fs.Args()
	if len(rest) < 1 {
		return usageError("get needs REMOTE... [LOCAL]")
	}
	conn, target, err := a.open(c)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	ctx := context.Background()
	base := target.Home()

	// One plain source and an optional destination keeps the original
	// meaning. Several sources, or a wildcard, make the last operand a
	// destination directory.
	multi := len(rest) > 2 || hasGlob(rest[0]) || outDir != ""
	if !multi {
		remote := resolveRemote(base, rest[0])
		local := ""
		if len(rest) == 2 {
			local = rest[1]
		}
		entry, err := conn.FS().Stat(ctx, remote)
		if err != nil {
			return err
		}
		if entry.IsDirLike() {
			if local == "" {
				local = path.Base(strings.TrimRight(remote, "/"))
			}
			if info, statErr := os.Stat(local); statErr == nil && info.IsDir() {
				local = filepath.Join(local, path.Base(strings.TrimRight(remote, "/")))
			}
		} else {
			if local == "" {
				local = path.Base(remote)
			}
			if info, statErr := os.Stat(local); statErr == nil && info.IsDir() {
				local = filepath.Join(local, path.Base(remote))
			}
		}
		return a.getOne(ctx, conn, remote, local, entry, c, recursive, force, resume)
	}

	operands, destDir := rest, "."
	switch {
	case outDir != "":
		destDir = outDir
	case len(rest) >= 2:
		operands, destDir = rest[:len(rest)-1], rest[len(rest)-1]
	}
	if info, statErr := os.Stat(destDir); statErr == nil && !info.IsDir() {
		return usageError("%s is not a directory", destDir)
	}
	if outDir != "" {
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			return err
		}
	}
	remotes, err := expandRemoteAll(ctx, conn.FS(), base, operands)
	if err != nil {
		return err
	}
	for _, remote := range remotes {
		entry, err := conn.FS().Stat(ctx, remote)
		if err != nil {
			return err
		}
		local := filepath.Join(destDir, path.Base(strings.TrimRight(remote, "/")))
		if err := a.getOne(ctx, conn, remote, local, entry, c, recursive, force, resume); err != nil {
			return err
		}
	}
	return nil
}

// getOne downloads a single resolved remote path to an exact local path.
func (a App) getOne(ctx context.Context, conn session.Conn, remote, local string, entry domain.Entry, c *connFlags, recursive, force, resume bool) error {
	if entry.IsDirLike() {
		if !recursive {
			return usageError("%s is a directory: pass -r to download it", remote)
		}
		return a.downloadTree(ctx, conn.FS(), conn.Engine(), remote, local, c, force, resume)
	}
	return a.downloadFile(ctx, conn.Engine(), remote, local, entry, c, force, resume)
}

func (a App) downloadFile(ctx context.Context, eng transfer.Engine, remote, local string, entry domain.Entry, c *connFlags, force, resume bool) error {
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return err
	}
	var offset int64
	if info, err := os.Stat(local); err == nil {
		if info.IsDir() {
			return fmt.Errorf("%s is a directory", local)
		}
		if !force && !resume {
			return fmt.Errorf("%s already exists (use --force to overwrite, --resume to continue)", local)
		}
		if resume {
			offset = info.Size()
			if entry.Size > 0 && offset >= entry.Size {
				if !c.quiet {
					a.statusf("= %s already complete", local)
				}
				return nil
			}
		}
	}
	req := transfer.Request{
		Direction:   domain.Download,
		Source:      remote,
		Destination: local,
		Size:        entry.Size,
		Offset:      offset,
		Limit:       c.limit,
	}
	if !c.quiet {
		a.statusf("downloading %s → %s", remote, local)
	}
	onProgress, finish := a.progressLine(c, path.Base(remote), entry.Size, offset)
	err := transfer.Copy(ctx, eng, req, onProgress)
	finish()
	if err != nil {
		return fmt.Errorf("download %s: %w", remote, err)
	}
	if !c.quiet {
		a.resultf("%s", local)
	}
	return nil
}

func (a App) downloadTree(ctx context.Context, fsys vfs.FS, eng transfer.Engine, remoteRoot, localRoot string, c *connFlags, force, resume bool) error {
	type pair struct{ remote, local string }
	stack := []pair{{remoteRoot, localRoot}}
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if err := os.MkdirAll(it.local, 0o755); err != nil {
			return err
		}
		entries, err := fsys.List(ctx, it.remote, true)
		if err != nil {
			return err
		}
		for _, e := range entries {
			childRemote := fsys.Child(it.remote, e.Name)
			childLocal := filepath.Join(it.local, e.Name)
			if e.IsDir() {
				stack = append(stack, pair{childRemote, childLocal})
				continue
			}
			// A symlink to a directory is not followed, matching the UI's
			// mirror: following one could leave the tree behind, or loop.
			if e.LinksToDir {
				if !c.quiet {
					a.statusf("skipping symlinked directory %s", childRemote)
				}
				continue
			}
			if err := a.downloadFile(ctx, eng, childRemote, childLocal, e, c, force, resume); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a App) cmdPut(args []string) error {
	fs := a.newFlagSet("put")
	var recursive, force, parents, noPart bool
	fs.BoolVar(&recursive, "r", false, "upload directories recursively")
	fs.BoolVar(&recursive, "recursive", false, "upload directories recursively")
	fs.BoolVar(&force, "f", false, "overwrite an existing destination")
	fs.BoolVar(&force, "force", false, "overwrite an existing destination")
	fs.BoolVar(&parents, "p", false, "create parent directories")
	fs.BoolVar(&parents, "parents", false, "create parent directories")
	fs.BoolVar(&noPart, "no-part", false, "write straight to the final name instead of NAME.part then rename")
	var resumeUp bool
	var outDir string
	fs.StringVar(&outDir, "O", "", "upload into this remote directory")
	fs.BoolVar(&resumeUp, "resume", false, "continue a partly uploaded file")
	fs.BoolVar(&resumeUp, "c", false, "same as --resume")
	c := &connFlags{}
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	rest := fs.Args()
	if len(rest) < 1 {
		return usageError("put needs LOCAL... [REMOTE]")
	}
	c.noPart = noPart
	c.resume = resumeUp

	// Sources are everything but the last operand when there are two or more;
	// the lone-operand form uploads into the base directory.
	sourceOperands, destOperand := rest, ""
	switch {
	case outDir != "":
		destOperand = outDir
	case len(rest) >= 2:
		sourceOperands, destOperand = rest[:len(rest)-1], rest[len(rest)-1]
	}
	var sources []string
	for _, op := range sourceOperands {
		matches, err := expandLocal(op)
		if err != nil {
			return err
		}
		sources = append(sources, matches...)
	}

	conn, target, err := a.open(c)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	ctx := context.Background()
	base := target.Home()
	// Several sources (or a wildcard) always mean "into this directory".
	intoDir := len(sources) > 1 || (len(rest) >= 2 && len(sourceOperands) > 1) || outDir != ""
	if intoDir {
		dest := base
		if destOperand != "" {
			dest = resolveRemote(base, destOperand)
		}
		if st, statErr := conn.FS().Stat(ctx, dest); statErr == nil && !st.IsDirLike() {
			return usageError("%s is not a directory", dest)
		} else if statErr != nil {
			if !parents {
				return fmt.Errorf("%s: %w (pass -p to create it)", dest, statErr)
			}
			if err := ensureRemoteDir(ctx, conn.FS(), dest); err != nil {
				return err
			}
		}
		for _, local := range sources {
			remote := vfs.ChildRemote(dest, path.Base(filepath.ToSlash(local)))
			if err := a.putOne(ctx, conn, local, remote, c, recursive, force, parents); err != nil {
				return err
			}
		}
		return nil
	}

	local := sources[0]
	var remote string
	if destOperand != "" {
		remote = resolveRemote(base, destOperand)
	} else {
		remote = vfs.ChildRemote(base, path.Base(filepath.ToSlash(local)))
	}
	if info, err := os.Lstat(local); err == nil && !info.IsDir() {
		// A remote destination that is an existing directory means "put it inside".
		if st, statErr := conn.FS().Stat(ctx, remote); statErr == nil && st.IsDirLike() {
			remote = vfs.ChildRemote(remote, path.Base(filepath.ToSlash(local)))
		}
	}
	return a.putOne(ctx, conn, local, remote, c, recursive, force, parents)
}

// putOne uploads one local file or tree to an exact remote path.
func (a App) putOne(ctx context.Context, conn session.Conn, local, remote string, c *connFlags, recursive, force, parents bool) error {
	info, err := os.Lstat(local)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if !recursive {
			return usageError("%s is a directory: pass -r to upload it", local)
		}
		return a.uploadTree(ctx, conn.FS(), conn.Engine(), local, remote, c, force, parents)
	}
	return a.uploadFile(ctx, conn.FS(), conn.Engine(), local, remote, info.Size(), c, force, parents)
}

func (a App) uploadFile(ctx context.Context, fsys vfs.FS, eng transfer.Engine, local, remote string, size int64, c *connFlags, force, parents bool) error {
	if parents {
		if err := ensureRemoteDir(ctx, fsys, path.Dir(remote)); err != nil {
			return err
		}
	}
	if st, err := fsys.Stat(ctx, remote); err == nil {
		if c.resume && !st.IsDir() && st.Size == size {
			if !c.quiet {
				a.statusf("= %s already complete", remote)
			}
			return nil
		}
		if !force {
			return fmt.Errorf("%s already exists (use --force to overwrite)", remote)
		}
	}
	// Upload to NAME.part and rename on success, so a reader of the
	// destination never sees a half-written file and a failed run leaves the
	// old one untouched.
	dest := remote
	if !c.noPart {
		dest = remote + ".part"
	}
	var offset int64
	if c.resume {
		if st, err := fsys.Stat(ctx, dest); err == nil && !st.IsDir() && st.Size < size {
			offset = st.Size
		}
	}
	req := transfer.Request{
		Direction:   domain.Upload,
		Source:      local,
		Destination: dest,
		Size:        size,
		Offset:      offset,
		Limit:       c.limit,
	}
	if !c.quiet {
		a.statusf("uploading %s → %s", local, remote)
	}
	onProgress, finish := a.progressLine(c, filepath.Base(local), size, offset)
	err := transfer.Copy(ctx, eng, req, onProgress)
	finish()
	if err != nil {
		if dest != remote && !c.resume {
			_ = fsys.Remove(ctx, dest)
		}
		return fmt.Errorf("upload %s: %w", local, err)
	}
	if dest != remote {
		if _, err := fsys.Stat(ctx, remote); err == nil {
			if err := fsys.Remove(ctx, remote); err != nil {
				return fmt.Errorf("replace %s: %w", remote, err)
			}
		}
		if err := fsys.Rename(ctx, dest, remote); err != nil {
			return fmt.Errorf("finish %s: %w", remote, err)
		}
	}
	if !c.quiet {
		a.resultf("%s", remote)
	}
	return nil
}

func (a App) uploadTree(ctx context.Context, fsys vfs.FS, eng transfer.Engine, localRoot, remoteRoot string, c *connFlags, force, parents bool) error {
	return filepath.WalkDir(localRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(localRoot, p)
		if err != nil {
			return err
		}
		remote := remoteRoot
		if rel != "." {
			remote = vfs.ChildRemote(remoteRoot, filepath.ToSlash(rel))
		}
		// Symlinks are not followed: a symlinked directory could leave the
		// tree, and an upload of the link itself has no portable meaning.
		if d.Type()&os.ModeSymlink != 0 {
			if !c.quiet {
				a.statusf("skipping symlink %s", p)
			}
			return nil
		}
		if d.IsDir() {
			return ensureRemoteDir(ctx, fsys, remote)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return a.uploadFile(ctx, fsys, eng, p, remote, info.Size(), c, force, parents)
	})
}

// ensureRemoteDir creates dir and every missing ancestor. An already-existing
// directory is not an error, which is what makes it safe to call repeatedly
// during a recursive upload.
func ensureRemoteDir(ctx context.Context, fsys vfs.FS, dir string) error {
	dir = vfs.CleanRemote(dir)
	if dir == "/" {
		return nil
	}
	if _, err := fsys.Stat(ctx, dir); err == nil {
		return nil
	}
	if err := ensureRemoteDir(ctx, fsys, path.Dir(dir)); err != nil {
		return err
	}
	if err := fsys.Mkdir(ctx, dir); err != nil && !errors.Is(err, vfs.ErrExists) {
		return err
	}
	return nil
}

func (a App) cmdRm(args []string) error {
	fs := a.newFlagSet("rm")
	var recursive bool
	fs.BoolVar(&recursive, "r", false, "delete directories and their contents")
	fs.BoolVar(&recursive, "recursive", false, "delete directories and their contents")
	c := &connFlags{}
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() == 0 {
		return usageError("rm needs at least one PATH")
	}
	conn, target, err := a.open(c)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	ctx := context.Background()
	base := target.Home()
	paths, err := expandRemoteAll(ctx, conn.FS(), base, fs.Args())
	if err != nil {
		return err
	}
	for _, remote := range paths {
		if err := a.removePath(ctx, conn.FS(), remote, recursive); err != nil {
			return err
		}
		if !c.quiet {
			a.resultf("%s", remote)
		}
	}
	return nil
}

func (a App) removePath(ctx context.Context, fsys vfs.FS, target string, recursive bool) error {
	entry, err := fsys.Stat(ctx, target)
	if err != nil {
		return err
	}
	if entry.IsDir() {
		if !recursive {
			return usageError("%s is a directory: pass -r to delete it", target)
		}
		entries, err := fsys.List(ctx, target, true)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := a.removePath(ctx, fsys, fsys.Child(target, e.Name), true); err != nil {
				return err
			}
		}
	}
	return fsys.Remove(ctx, target)
}

func (a App) cmdMkdir(args []string) error {
	fs := a.newFlagSet("mkdir")
	var parents bool
	fs.BoolVar(&parents, "p", false, "create parent directories as needed")
	fs.BoolVar(&parents, "parents", false, "create parent directories as needed")
	c := &connFlags{}
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() == 0 {
		return usageError("mkdir needs at least one PATH")
	}
	conn, target, err := a.open(c)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	ctx := context.Background()
	base := target.Home()
	for _, p := range fs.Args() {
		remote := resolveRemote(base, p)
		if parents {
			if err := ensureRemoteDir(ctx, conn.FS(), remote); err != nil {
				return err
			}
		} else if err := conn.FS().Mkdir(ctx, remote); err != nil {
			return err
		}
		if !c.quiet {
			a.resultf("%s", remote)
		}
	}
	return nil
}

func (a App) cmdMv(args []string) error {
	fs := a.newFlagSet("mv")
	c := &connFlags{}
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() != 2 {
		return usageError("mv needs OLD and NEW")
	}
	conn, target, err := a.open(c)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	base := target.Home()
	from := resolveRemote(base, fs.Arg(0))
	to := resolveRemote(base, fs.Arg(1))
	if err := conn.FS().Rename(context.Background(), from, to); err != nil {
		return err
	}
	if !c.quiet {
		a.resultf("%s -> %s", from, to)
	}
	return nil
}
