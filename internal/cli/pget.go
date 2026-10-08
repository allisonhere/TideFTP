package cli

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"tideftp/internal/domain"
	"tideftp/internal/session"
	"tideftp/internal/transfer"
	"tideftp/internal/vfs"
)

// minSegmentBytes is the smallest slice worth its own connection; a file
// smaller than two of these is fetched in one piece.
const minSegmentBytes = 1 << 20

// cmdPget downloads one file over several connections at once, each fetching
// its own byte range into a shared preallocated file (lftp's pget -n).
func (a App) cmdPget(args []string) error {
	fset := a.newFlagSet("pget")
	var segments int
	var force bool
	fset.IntVar(&segments, "n", 4, "number of connections to fetch the file over")
	fset.BoolVar(&force, "force", false, "overwrite an existing destination")
	fset.BoolVar(&force, "f", false, "overwrite an existing destination")
	c := &connFlags{}
	c.register(fset)
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	if fset.NArg() < 1 || fset.NArg() > 2 {
		return usageError("pget needs REMOTE [LOCAL]")
	}
	if segments < 1 {
		return usageError("-n must be at least 1")
	}
	conn, target, creds, err := a.openWith(c)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	ctx := context.Background()
	remote := resolveRemote(target.Home(), fset.Arg(0))
	entry, err := conn.FS().Stat(ctx, remote)
	if err != nil {
		return err
	}
	if entry.IsDirLike() {
		return usageError("%s is a directory; pget fetches single files", remote)
	}
	local := fset.Arg(1)
	if local == "" {
		local = path.Base(remote)
	}
	if info, statErr := os.Stat(local); statErr == nil {
		if info.IsDir() {
			local = filepath.Join(local, path.Base(remote))
		} else if !force {
			return fmt.Errorf("%s already exists (use --force to overwrite)", local)
		}
	}
	more := func() (session.Conn, error) { return a.connectLive(target, creds, c) }
	if !c.quiet {
		a.statusf("downloading %s → %s (%d connections)", remote, local, segments)
	}
	if err := segmentedDownload(ctx, conn, more, remote, local, entry, segments, c.limit); err != nil {
		return fmt.Errorf("download %s: %w", remote, err)
	}
	if !c.quiet {
		a.resultf("%s", local)
	}
	return nil
}

// segmentedDownload fetches remote into local using up to segments
// connections: the first is primary, the rest come from more and are closed
// afterwards. A file too small to split, or segments of 1, is an ordinary
// single-stream download.
func segmentedDownload(ctx context.Context, primary session.Conn, more func() (session.Conn, error), remote, local string, entry domain.Entry, segments int, limit *transfer.Limiter) error {
	size := entry.Size
	n := segments
	if size > 0 {
		n = min(n, int(size/minSegmentBytes))
	}
	if n < 2 || size <= 0 {
		return downloadAtomic(ctx, primary.Engine(), remote, local, entry, false, limit)
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return err
	}
	part := local + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		_ = os.Remove(part)
		return err
	}
	_ = f.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	chunk := size / int64(n)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
			cancel() // the other segments cannot make the file whole now
		}
		mu.Unlock()
	}
	for i := 0; i < n; i++ {
		start, length := int64(i)*chunk, chunk
		if i == n-1 {
			length = size - start
		}
		wg.Add(1)
		go func(i int, start, length int64) {
			defer wg.Done()
			conn := primary
			if i > 0 {
				c, err := more()
				if err != nil {
					fail(fmt.Errorf("segment %d: %w", i+1, err))
					return
				}
				defer func() { _ = c.Close() }()
				conn = c
			}
			req := transfer.Request{
				Direction: domain.Download, Source: remote, Destination: part,
				Size: size, Offset: start, Length: length, NoTruncate: true, Limit: limit,
			}
			if err := transfer.Copy(ctx, conn.Engine(), req, nil); err != nil {
				fail(fmt.Errorf("segment %d: %w", i+1, err))
			}
		}(i, start, length)
	}
	wg.Wait()
	if firstErr != nil {
		_ = os.Remove(part)
		return firstErr
	}
	_ = os.Remove(local)
	if err := os.Rename(part, local); err != nil {
		return err
	}
	if !entry.Modified.IsZero() {
		_ = os.Chtimes(local, entry.Modified, entry.Modified)
	}
	return nil
}

// cmdRmdir removes empty directories.
func (a App) cmdRmdir(args []string) error {
	fset := a.newFlagSet("rmdir")
	c := &connFlags{}
	c.register(fset)
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	if fset.NArg() == 0 {
		return usageError("rmdir needs at least one PATH")
	}
	conn, target, err := a.open(c)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	ctx := context.Background()
	paths, err := expandRemoteAll(ctx, conn.FS(), target.Home(), fset.Args())
	if err != nil {
		return err
	}
	for _, p := range paths {
		entry, err := conn.FS().Stat(ctx, p)
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return usageError("%s is not a directory", p)
		}
		if err := conn.FS().Remove(ctx, p); err != nil {
			return fmt.Errorf("rmdir %s: %w", p, err)
		}
		if !c.quiet {
			a.resultf("%s", p)
		}
	}
	return nil
}

// cmdLn makes a symbolic link on the server: ln -s TARGET LINK. Hard links are
// not offered by the file-transfer protocols this speaks.
func (a App) cmdLn(args []string) error {
	fset := a.newFlagSet("ln")
	var symbolic, force bool
	fset.BoolVar(&symbolic, "s", false, "make a symbolic link (required)")
	fset.BoolVar(&force, "f", false, "replace an existing link")
	c := &connFlags{}
	c.register(fset)
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	if !symbolic {
		return usageError("ln only makes symbolic links: use ln -s TARGET LINK")
	}
	if fset.NArg() != 2 {
		return usageError("ln -s needs TARGET and LINK")
	}
	conn, target, err := a.open(c)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	ctx := context.Background()
	linker, ok := conn.FS().(vfs.Symlinker)
	if !ok {
		return fmt.Errorf("ln: %w", vfs.ErrUnsupported)
	}
	link := resolveRemote(target.Home(), fset.Arg(1))
	// Like ln(1): an existing directory as LINK means "inside it". A symlink to
	// a directory does not count (that is ln -n): it is the thing -f replaces,
	// which is how `ln -sf release current` repoints a deploy.
	if st, err := conn.FS().Stat(ctx, link); err == nil && st.IsDir() {
		link = vfs.ChildRemote(link, path.Base(strings.TrimRight(fset.Arg(0), "/")))
	}
	if force {
		if st, err := conn.FS().Stat(ctx, link); err == nil && st.Kind == domain.EntrySymlink {
			if err := conn.FS().Remove(ctx, link); err != nil {
				return err
			}
		}
	}
	if err := linker.Symlink(ctx, fset.Arg(0), link); err != nil {
		return fmt.Errorf("ln -s %s %s: %w", fset.Arg(0), link, err)
	}
	if !c.quiet {
		a.resultf("%s -> %s", link, fset.Arg(0))
	}
	return nil
}

// cmdReadlink prints where a symbolic link points.
func (a App) cmdReadlink(args []string) error {
	fset := a.newFlagSet("readlink")
	c := &connFlags{}
	c.register(fset)
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	if fset.NArg() != 1 {
		return usageError("readlink needs one PATH")
	}
	conn, target, err := a.open(c)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	linker, ok := conn.FS().(vfs.Symlinker)
	if !ok {
		return fmt.Errorf("readlink: %w", vfs.ErrUnsupported)
	}
	dest, err := linker.Readlink(context.Background(), resolveRemote(target.Home(), fset.Arg(0)))
	if err != nil {
		return err
	}
	a.resultf("%s", dest)
	return nil
}
