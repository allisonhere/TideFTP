package sftpsession

import (
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/pkg/sftp"

	"tideftp/internal/domain"
	"tideftp/internal/vfs"
)

var _ vfs.FS = (*FS)(nil)

// FS browses the server over an sftp.Client, which is safe for concurrent use,
// so both panes and the transfer engine share one.
type FS struct {
	client *sftp.Client
}

// List reads a directory. pkg/sftp has no context-aware API, so the read runs
// on its own goroutine and ctx only decides how long to wait for it: on
// cancellation List returns, the caller is unblocked, and the orphaned read
// finishes into a discarded channel. That is enough to keep the UI responsive,
// which is the point; the underlying request still occupies the connection
// until the server answers or the connection closes.
func (f *FS) List(ctx context.Context, dirPath string, showHidden bool) ([]domain.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dirPath = cleanPath(dirPath)

	type result struct {
		entries []domain.Entry
		err     error
	}
	done := make(chan result, 1)
	go func() {
		infos, err := f.client.ReadDir(dirPath)
		if err != nil {
			done <- result{err: fmt.Errorf("list %s: %w", dirPath, err)}
			return
		}
		entries := make([]domain.Entry, 0, len(infos))
		for _, info := range infos {
			name := info.Name()
			hidden := strings.HasPrefix(name, ".")
			if hidden && !showHidden {
				continue
			}
			entries = append(entries, domain.Entry{
				Name:     name,
				Kind:     entryKind(info.Mode()),
				Size:     info.Size(),
				Mode:     info.Mode().String(),
				Modified: info.ModTime(),
				Hidden:   hidden,
			})
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Kind != entries[j].Kind {
				return entries[i].Kind == domain.EntryDir
			}
			return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
		})
		done <- result{entries: entries}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case out := <-done:
		return out.entries, out.err
	}
}

func (f *FS) Child(current, name string) string {
	return cleanPath(path.Join(cleanPath(current), name))
}

// Parent returns current unchanged at the root, which is how the UI knows
// there is nowhere further up to go.
func (f *FS) Parent(current string) string {
	parent := path.Dir(cleanPath(current))
	if parent == "." {
		return "/"
	}
	return parent
}

func entryKind(mode os.FileMode) domain.EntryKind {
	switch {
	case mode.IsDir():
		return domain.EntryDir
	case mode&os.ModeSymlink != 0:
		return domain.EntrySymlink
	default:
		return domain.EntryFile
	}
}

// cleanPath normalises a remote path to an absolute POSIX one. Remote paths are
// always slash-separated regardless of the client's own OS, so path is correct
// here and filepath would not be.
func cleanPath(value string) string {
	if strings.TrimSpace(value) == "" {
		return "/"
	}
	return path.Clean("/" + strings.TrimPrefix(value, "/"))
}
