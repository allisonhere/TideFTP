package cli

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"tideftp/internal/domain"
	"tideftp/internal/vfs"
)

// cmdCat streams remote files to stdout, so they can be piped.
func (a App) cmdCat(args []string) error {
	fset := a.newFlagSet("cat")
	c := &connFlags{}
	c.register(fset)
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	if fset.NArg() == 0 {
		return usageError("cat needs at least one PATH")
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
		if entry.IsDirLike() {
			return usageError("%s is a directory", p)
		}
		rc, err := conn.FS().Open(ctx, p)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(a.Stdout, rc)
		_ = rc.Close()
		if copyErr != nil {
			return fmt.Errorf("cat %s: %w", p, copyErr)
		}
	}
	return nil
}

// cmdDu totals sizes under each PATH. By default it prints one line per
// operand; -d N also prints every directory down to depth N.
func (a App) cmdDu(args []string) error {
	fset := a.newFlagSet("du")
	var human bool
	var maxDepth int
	fset.BoolVar(&human, "h", false, "human-readable sizes")
	fset.IntVar(&maxDepth, "d", 0, "also print directories down to this depth")
	c := &connFlags{}
	c.register(fset)
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	conn, target, err := a.open(c)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	ctx := context.Background()
	operands := fset.Args()
	if len(operands) == 0 {
		operands = []string{target.Home()}
	}
	paths, err := expandRemoteAll(ctx, conn.FS(), target.Home(), operands)
	if err != nil {
		return err
	}
	show := func(size int64, p string) {
		if c.quiet {
			return
		}
		if human {
			a.resultf("%s\t%s", humanBytes(size), p)
		} else {
			a.resultf("%d\t%s", size, p)
		}
	}
	for _, p := range paths {
		entry, err := conn.FS().Stat(ctx, p)
		if err != nil {
			return err
		}
		if !entry.IsDirLike() {
			show(entry.Size, p)
			continue
		}
		total, err := duWalk(ctx, conn.FS(), p, 0, maxDepth, show)
		if err != nil {
			return err
		}
		show(total, p)
	}
	return nil
}

// duWalk returns the total size under dir, calling emit for each
// subdirectory within depth levels, deepest first like du(1).
func duWalk(ctx context.Context, fsys vfs.FS, dir string, depth, maxDepth int, emit func(int64, string)) (int64, error) {
	entries, err := fsys.List(ctx, dir, true)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", dir, err)
	}
	var total int64
	for _, e := range entries {
		switch {
		case e.IsDir():
			child := fsys.Child(dir, e.Name)
			sub, err := duWalk(ctx, fsys, child, depth+1, maxDepth, emit)
			if err != nil {
				return 0, err
			}
			total += sub
			if depth+1 <= maxDepth {
				emit(sub, child)
			}
		case e.LinksToDir:
		default:
			total += e.Size
		}
	}
	return total, nil
}

// cmdFind lists paths under a directory that match the given tests.
func (a App) cmdFind(args []string) error {
	fset := a.newFlagSet("find")
	var name, kind, minSize, maxSize, minAge, maxAge string
	var maxDepth int
	var asJSON bool
	fset.StringVar(&name, "name", "", "match the file name against this glob")
	fset.StringVar(&kind, "type", "", "f (file), d (directory) or l (symlink)")
	fset.StringVar(&minSize, "min-size", "", "files at least this big (10k, 5M)")
	fset.StringVar(&maxSize, "max-size", "", "files at most this big")
	fset.StringVar(&minAge, "min-age", "", "modified at least this long ago (2h, 7d)")
	fset.StringVar(&maxAge, "max-age", "", "modified at most this long ago")
	fset.IntVar(&maxDepth, "maxdepth", -1, "do not descend below this level (-1 = unlimited)")
	fset.BoolVar(&asJSON, "json", false, "print a JSON array")
	c := &connFlags{}
	c.register(fset)
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	if _, err := path.Match(name, ""); err != nil {
		return usageError("bad --name pattern: %v", err)
	}
	if kind != "" && kind != "f" && kind != "d" && kind != "l" {
		return usageError("--type must be f, d or l")
	}
	f := syncFilter{now: time.Now()}
	var err error
	if f.minSize, err = parseSize(minSize); err != nil {
		return usageError("--min-size: %v", err)
	}
	if f.maxSize, err = parseSize(maxSize); err != nil {
		return usageError("--max-size: %v", err)
	}
	if f.minAge, err = parseAge(minAge); err != nil {
		return usageError("--min-age: %v", err)
	}
	if f.maxAge, err = parseAge(maxAge); err != nil {
		return usageError("--max-age: %v", err)
	}
	if fset.NArg() > 1 {
		return usageError("find takes one starting PATH")
	}

	conn, target, err := a.open(c)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	ctx := context.Background()
	start := target.Home()
	if fset.NArg() == 1 {
		start = resolveRemote(start, fset.Arg(0))
	}
	root, err := conn.FS().Stat(ctx, start)
	if err != nil {
		return err
	}
	if !root.IsDirLike() {
		return usageError("%s is not a directory", start)
	}

	matches := func(e domain.Entry) bool {
		switch kind {
		case "f":
			if e.IsDir() || e.Kind == domain.EntrySymlink {
				return false
			}
		case "d":
			if !e.IsDir() {
				return false
			}
		case "l":
			if e.Kind != domain.EntrySymlink {
				return false
			}
		}
		if name != "" {
			if ok, _ := path.Match(name, e.Name); !ok {
				return false
			}
		}
		// Size and age tests are about files; a directory's own "size" is
		// whatever the server reports and would only confuse them.
		if !e.IsDir() {
			return f.selects(e.Name, e)
		}
		return f.minSize == 0 && f.maxSize == 0
	}

	rows := []entryJSON{}
	type item struct {
		dir   string
		depth int
	}
	stack := []item{{start, 1}}
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := conn.FS().List(ctx, it.dir, true)
		if err != nil {
			return fmt.Errorf("read %s: %w", it.dir, err)
		}
		for _, e := range entries {
			full := conn.FS().Child(it.dir, e.Name)
			if matches(e) {
				rows = append(rows, toEntryJSON(full, e))
			}
			if e.IsDir() && (maxDepth < 0 || it.depth < maxDepth) {
				stack = append(stack, item{full, it.depth + 1})
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })
	if asJSON {
		return a.printJSON(rows)
	}
	if c.quiet {
		return nil
	}
	for _, r := range rows {
		a.resultf("%s", r.Path)
	}
	return nil
}

// cmdTree prints an indented tree of a directory.
func (a App) cmdTree(args []string) error {
	fset := a.newFlagSet("tree")
	var depth int
	var dirsOnly bool
	fset.IntVar(&depth, "L", -1, "descend only this many levels")
	fset.BoolVar(&dirsOnly, "d", false, "list directories only")
	c := &connFlags{}
	c.register(fset)
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	if fset.NArg() > 1 {
		return usageError("tree takes one PATH")
	}
	conn, target, err := a.open(c)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	ctx := context.Background()
	start := target.Home()
	if fset.NArg() == 1 {
		start = resolveRemote(start, fset.Arg(0))
	}
	root, err := conn.FS().Stat(ctx, start)
	if err != nil {
		return err
	}
	if !root.IsDirLike() {
		return usageError("%s is not a directory", start)
	}
	var dirs, files int
	var lines []string
	var walk func(dir, prefix string, level int) error
	walk = func(dir, prefix string, level int) error {
		entries, err := conn.FS().List(ctx, dir, true)
		if err != nil {
			return fmt.Errorf("read %s: %w", dir, err)
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsDir() != entries[j].IsDir() {
				return entries[i].IsDir()
			}
			return entries[i].Name < entries[j].Name
		})
		if dirsOnly {
			kept := entries[:0]
			for _, e := range entries {
				if e.IsDir() {
					kept = append(kept, e)
				}
			}
			entries = kept
		}
		for i, e := range entries {
			branch, next := "├── ", "│   "
			if i == len(entries)-1 {
				branch, next = "└── ", "    "
			}
			name := e.Name
			if e.IsDir() {
				name += "/"
				dirs++
			} else {
				files++
			}
			lines = append(lines, prefix+branch+name)
			if e.IsDir() && (depth < 0 || level < depth) {
				if err := walk(conn.FS().Child(dir, e.Name), prefix+next, level+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(start, "", 1); err != nil {
		return err
	}
	if c.quiet {
		return nil
	}
	a.resultf("%s", start)
	for _, l := range lines {
		a.resultf("%s", l)
	}
	if dirsOnly {
		a.resultf("\n%d directories", dirs)
	} else {
		a.resultf("\n%d directories, %d files", dirs, files)
	}
	return nil
}

// cmdChmod sets permissions with an octal mode (755) or a symbolic one
// (u+x,go-w), optionally through a whole tree.
func (a App) cmdChmod(args []string) error {
	fset := a.newFlagSet("chmod")
	var recursive bool
	fset.BoolVar(&recursive, "R", false, "apply to directories and everything inside them")
	c := &connFlags{}
	c.register(fset)
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	if fset.NArg() < 2 {
		return usageError("chmod needs MODE and at least one PATH")
	}
	spec := fset.Arg(0)
	if _, err := parseMode(spec, 0o644); err != nil {
		return usageError("%v", err)
	}
	conn, target, err := a.open(c)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	ctx := context.Background()
	paths, err := expandRemoteAll(ctx, conn.FS(), target.Home(), fset.Args()[1:])
	if err != nil {
		return err
	}
	var apply func(p string, e domain.Entry) error
	apply = func(p string, e domain.Entry) error {
		if e.Kind == domain.EntrySymlink && !e.LinksToDir {
			return nil // chmod on a link would change its target
		}
		cur, haveCur := currentPerm(e.Mode)
		mode, err := parseMode(spec, cur)
		if err != nil {
			return err
		}
		if !haveCur && !isOctal(spec) {
			return fmt.Errorf("%s: the server does not report permissions; use an octal mode", p)
		}
		if err := conn.FS().Chmod(ctx, p, mode); err != nil {
			return fmt.Errorf("chmod %s: %w", p, err)
		}
		if !c.quiet {
			a.resultf("%04o %s", uint32(mode.Perm())|specialBits(mode), p)
		}
		if recursive && e.IsDir() {
			children, err := conn.FS().List(ctx, p, true)
			if err != nil {
				return fmt.Errorf("read %s: %w", p, err)
			}
			for _, ch := range children {
				if ch.LinksToDir {
					continue
				}
				if err := apply(conn.FS().Child(p, ch.Name), ch); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, p := range paths {
		e, err := conn.FS().Stat(ctx, p)
		if err != nil {
			return err
		}
		if err := apply(p, e); err != nil {
			return err
		}
	}
	return nil
}

func specialBits(m fs.FileMode) uint32 {
	var v uint32
	if m&fs.ModeSetuid != 0 {
		v |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		v |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		v |= 0o1000
	}
	return v
}

func isOctal(spec string) bool {
	if len(spec) < 3 || len(spec) > 4 {
		return false
	}
	for _, r := range spec {
		if r < '0' || r > '7' {
			return false
		}
	}
	return true
}

// currentPerm reads the permission bits out of a listing's mode string
// ("-rw-r--r--", "drwxr-xr-x"). ok is false for a server that reports
// something else, such as FTP's plain "file".
func currentPerm(mode string) (fs.FileMode, bool) {
	if len(mode) < 9 {
		return 0, false
	}
	var perm fs.FileMode
	for i, ch := range mode[len(mode)-9:] {
		slot := "rwx"[i%3]
		switch {
		case ch == '-' || ch == 'S' || ch == 'T':
		case byte(ch) == slot || (slot == 'x' && (ch == 's' || ch == 't')):
			perm |= 1 << (8 - uint(i))
		default:
			return 0, false
		}
	}
	return perm, true
}

// parseMode resolves an octal (644, 0755, 1777) or symbolic (u+x,go-w,a=r)
// spec against the current permissions.
func parseMode(spec string, cur fs.FileMode) (fs.FileMode, error) {
	if isOctal(spec) {
		var v uint32
		for _, r := range spec {
			v = v<<3 | uint32(r-'0')
		}
		m := fs.FileMode(v & 0o777)
		if v&0o4000 != 0 {
			m |= fs.ModeSetuid
		}
		if v&0o2000 != 0 {
			m |= fs.ModeSetgid
		}
		if v&0o1000 != 0 {
			m |= fs.ModeSticky
		}
		return m, nil
	}
	perm := cur.Perm()
	for _, clause := range strings.Split(spec, ",") {
		i := strings.IndexAny(clause, "+-=")
		if i < 0 {
			return 0, fmt.Errorf("invalid mode %q", spec)
		}
		who, op, what := clause[:i], clause[i], clause[i+1:]
		var whoMask fs.FileMode
		if who == "" {
			who = "a"
		}
		for _, w := range who {
			switch w {
			case 'u':
				whoMask |= 0o700
			case 'g':
				whoMask |= 0o070
			case 'o':
				whoMask |= 0o007
			case 'a':
				whoMask |= 0o777
			default:
				return 0, fmt.Errorf("invalid mode %q", spec)
			}
		}
		var bits fs.FileMode
		for _, w := range what {
			switch w {
			case 'r':
				bits |= 0o444
			case 'w':
				bits |= 0o222
			case 'x':
				bits |= 0o111
			default:
				return 0, fmt.Errorf("invalid mode %q (only r, w, x are supported)", spec)
			}
		}
		bits &= whoMask
		switch op {
		case '+':
			perm |= bits
		case '-':
			perm &^= bits
		case '=':
			perm = perm&^whoMask | bits
		}
	}
	return perm, nil
}
