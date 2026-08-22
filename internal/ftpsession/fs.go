package ftpsession

import (
	"context"
	"fmt"
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
// long to wait for it; an abandoned listing finishes into a discarded channel
// and its connection is dropped rather than reused, since a control connection
// left mid-response would corrupt every later command on it.
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
	done := make(chan result, 1)
	abandoned := make(chan struct{})
	go func() {
		entries, err := conn.List(dirPath)
		select {
		case done <- result{entries: entries, err: err}:
		case <-abandoned:
			// Nobody is waiting any more, and this connection's state is
			// unknown, so it goes rather than returning to the pool.
			f.pool.discard(conn)
		}
	}()

	select {
	case <-ctx.Done():
		close(abandoned)
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
