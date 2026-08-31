// Package localfs browses the machine's own disk through the same vfs.FS
// interface the remote pane uses. Local reads are usually instant, but "usually"
// is doing real work there: a stalled network mount blocks os.ReadDir just as
// hard as a dead FTP server, so the local pane goes through the same
// off-goroutine path as the remote one.
package localfs

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"tideftp/internal/domain"
	"tideftp/internal/vfs"
)

var _ vfs.FS = FS{}

type FS struct{}

func New() FS { return FS{} }

func (FS) List(ctx context.Context, dirPath string, showHidden bool) ([]domain.Entry, error) {
	items, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}
	entries := make([]domain.Entry, 0, len(items))
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !showHidden && strings.HasPrefix(item.Name(), ".") {
			continue
		}
		info, err := item.Info()
		if err != nil {
			// The entry vanished between ReadDir and Info, which is normal in
			// a live directory. Skip it rather than failing the whole listing.
			continue
		}
		kind := domain.EntryFile
		if item.IsDir() {
			kind = domain.EntryDir
		} else if info.Mode()&os.ModeSymlink != 0 {
			kind = domain.EntrySymlink
		}
		entries = append(entries, domain.Entry{
			Name:     item.Name(),
			Kind:     kind,
			Size:     info.Size(),
			Mode:     info.Mode().String(),
			Modified: info.ModTime(),
			Hidden:   strings.HasPrefix(item.Name(), "."),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind == domain.EntryDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, nil
}

func (FS) Child(current, name string) string { return filepath.Join(current, name) }

// Parent returns current unchanged at the filesystem root, which is how the UI
// knows there is nowhere further up to go.
func (FS) Parent(current string) string { return filepath.Dir(current) }

func (FS) Mkdir(ctx context.Context, dirPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Mkdir(dirPath, 0o755)
}

func (FS) Rename(ctx context.Context, oldPath, newPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// os.Rename silently replaces an existing destination on Unix. Every vfs
	// backend refuses instead, so a rename never destroys a file the user
	// forgot was there; overwriting is a decision for a future confirm step.
	if _, err := os.Lstat(newPath); err == nil {
		return fmt.Errorf("%w: %s", vfs.ErrExists, newPath)
	}
	return os.Rename(oldPath, newPath)
}

func (FS) Remove(ctx context.Context, targetPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Remove(targetPath)
}

func (FS) Chmod(ctx context.Context, path string, mode fs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func (FS) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func (FS) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (FS) WriteFile(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// perm applies only when creating; an existing file keeps its mode.
	return os.WriteFile(path, data, 0o644)
}
