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
