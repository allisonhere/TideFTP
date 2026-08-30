package ftpsession

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/jlaffaye/ftp"

	"tideftp/internal/domain"
	"tideftp/internal/vfs"
)

var _ vfs.FS = (*FS)(nil)

// FS browses the server, borrowing a control connection per listing.
type FS struct {
	pool *pool
}

// List reads a directory. jlaffaye/ftp's calls are blocking with no context of
// their own, so the work runs on its own goroutine and ctx decides only how
// long to wait for it; an abandoned listing's connection is dropped rather
// than reused, since a control connection left mid-response would corrupt
// every later command on it.
func (f *FS) List(ctx context.Context, dirPath string, showHidden bool) ([]domain.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dirPath = vfs.CleanRemote(dirPath)

	conn, err := f.pool.get(ctx)
	if err != nil {
		return nil, err
	}

	type result struct {
		entries []*ftp.Entry
		err     error
	}
	// done is unconditionally sent to exactly once, never selected against
	// another case on the send side: a buffered send that could otherwise
	// "win" a race against ctx expiring is what let a listing's connection
	// go unclaimed by either branch below, leaking it and its pool slot
	// forever.
	done := make(chan result, 1)
	go func() {
		entries, err := conn.List(dirPath)
		done <- result{entries: entries, err: err}
	}()

	select {
	case <-ctx.Done():
		// The goroutine above still owns conn until conn.List returns, which
		// this cannot cancel. Hand its disposal to a cleanup goroutine that
		// waits for it, rather than leaking the connection and its pool slot.
		go func() {
			<-done
			f.pool.discard(conn)
		}()
		return nil, ctx.Err()
	case out := <-done:
		if out.err != nil {
			f.pool.discard(conn)
			return nil, fmt.Errorf("list %s: %w", dirPath, out.err)
		}
		f.pool.put(conn)
		return convertEntries(out.entries, showHidden), nil
	}
}

func (f *FS) Child(current, name string) string { return vfs.ChildRemote(current, name) }

func (f *FS) Parent(current string) string { return vfs.ParentRemote(current) }

func (f *FS) Mkdir(ctx context.Context, dirPath string) error {
	return f.withConn(ctx, func(conn *ftp.ServerConn) error {
		return conn.MakeDir(vfs.CleanRemote(dirPath))
	})
}

func (f *FS) Rename(ctx context.Context, oldPath, newPath string) error {
	oldPath, newPath = vfs.CleanRemote(oldPath), vfs.CleanRemote(newPath)
	return f.withConn(ctx, func(conn *ftp.ServerConn) error {
		// FTP servers disagree on whether RNFR/RNTO onto an existing name
		// overwrites or fails. Check first so every backend refuses alike.
		if ftpPathExists(conn, newPath) {
			return fmt.Errorf("%w: %s", vfs.ErrExists, newPath)
		}
		return conn.Rename(oldPath, newPath)
	})
}

// ftpPathExists reports whether name resolves to a file or directory. It
// prefers MLST (one round trip, exact path) and falls back to scanning the
// parent listing for servers that do not support it.
func ftpPathExists(conn *ftp.ServerConn, name string) bool {
	if entry, err := conn.GetEntry(name); err == nil && entry != nil {
		return true
	}
	entries, err := conn.List(path.Dir(name))
	if err != nil {
		return false
	}
	base := path.Base(name)
	for _, entry := range entries {
		if entry.Name == base {
			return true
		}
	}
	return false
}

func (f *FS) Remove(ctx context.Context, targetPath string) error {
	targetPath = vfs.CleanRemote(targetPath)
	return f.withConn(ctx, func(conn *ftp.ServerConn) error {
		if err := conn.Delete(targetPath); err == nil {
			return nil
		}
		return conn.RemoveDir(targetPath)
	})
}

func (f *FS) ReadFile(ctx context.Context, path string) ([]byte, error) {
	path = vfs.CleanRemote(path)
	var data []byte
	err := f.withConn(ctx, func(conn *ftp.ServerConn) error {
		resp, err := conn.Retr(path)
		if err != nil {
			return err
		}
		defer resp.Close()
		data, err = io.ReadAll(resp)
		return err
	})
	return data, err
}

// Open borrows a control connection for the whole life of the returned
// reader and hands it back on Close — a data transfer occupies its control
// connection until the transfer ends, so unlike every other FS call here
// the borrow cannot be scoped to withConn. A caller that forgets to Close
// leaks the connection and its pool slot, which is why the vfs.FS contract
// makes closing the caller's job in so many words.
func (f *FS) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path = vfs.CleanRemote(path)
	conn, err := f.pool.get(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := conn.Retr(path)
	if err != nil {
		f.pool.discard(conn)
		return nil, fmt.Errorf("retrieve %s: %w", path, err)
	}
	return &pooledReader{resp: resp, pool: f.pool, conn: conn}, nil
}

// pooledReader is Open's reader: the data connection's response, plus the
// control connection it is running on, returned to the pool on Close. A read
// error leaves the control stream mid-response, so the connection is
// discarded rather than reused — the same rule withConn follows.
type pooledReader struct {
	resp   *ftp.Response
	pool   *pool
	conn   *ftp.ServerConn
	failed bool
	closed bool
}

func (r *pooledReader) Read(p []byte) (int, error) {
	n, err := r.resp.Read(p)
	if err != nil && err != io.EOF {
		r.failed = true
	}
	return n, err
}

func (r *pooledReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	err := r.resp.Close()
	if err != nil || r.failed {
		r.pool.discard(r.conn)
		return err
	}
	r.pool.put(r.conn)
	return nil
}

func (f *FS) WriteFile(ctx context.Context, path string, data []byte) error {
	path = vfs.CleanRemote(path)
	return f.withConn(ctx, func(conn *ftp.ServerConn) error {
		return conn.Stor(path, bytes.NewReader(data))
	})
}

func (f *FS) withConn(ctx context.Context, fn func(*ftp.ServerConn) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, err := f.pool.get(ctx)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		done <- fn(conn)
	}()
	select {
	case <-ctx.Done():
		go func() {
			<-done
			f.pool.discard(conn)
		}()
		return ctx.Err()
	case err := <-done:
		if err != nil {
			f.pool.discard(conn)
			return err
		}
		f.pool.put(conn)
		return nil
	}
}

// convertEntries maps an FTP listing to the app's entries. LIST includes "."
// and ".." on most servers; the UI navigates with backspace and would show
// them as ordinary rows, so they are dropped.
func convertEntries(listing []*ftp.Entry, showHidden bool) []domain.Entry {
	entries := make([]domain.Entry, 0, len(listing))
	for _, item := range listing {
		if item == nil || item.Name == "." || item.Name == ".." {
			continue
		}
		hidden := strings.HasPrefix(item.Name, ".")
		if hidden && !showHidden {
			continue
		}
		entries = append(entries, domain.Entry{
			Name:     item.Name,
			Kind:     entryKind(item.Type),
			Size:     int64(item.Size),
			Mode:     modeLabel(item.Type),
			Modified: item.Time,
			Hidden:   hidden,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind == domain.EntryDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries
}

func entryKind(kind ftp.EntryType) domain.EntryKind {
	switch kind {
	case ftp.EntryTypeFolder:
		return domain.EntryDir
	case ftp.EntryTypeLink:
		return domain.EntrySymlink
	default:
		return domain.EntryFile
	}
}

// modeLabel stands in for a permission string. A plain LIST listing does not
// reliably carry one, and jlaffaye/ftp does not expose what it parsed, so the
// column shows the kind rather than inventing permissions.
func modeLabel(kind ftp.EntryType) string {
	switch kind {
	case ftp.EntryTypeFolder:
		return "dir"
	case ftp.EntryTypeLink:
		return "link"
	default:
		return "file"
	}
}
