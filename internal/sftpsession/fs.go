package sftpsession

import (
	"context"
	"fmt"
	"io"
	"os"
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
	dirPath = vfs.CleanRemote(dirPath)

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

func (f *FS) Child(current, name string) string { return vfs.ChildRemote(current, name) }

func (f *FS) Parent(current string) string { return vfs.ParentRemote(current) }

func (f *FS) Mkdir(ctx context.Context, dirPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.client.Mkdir(vfs.CleanRemote(dirPath))
}

func (f *FS) Rename(ctx context.Context, oldPath, newPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	oldPath, newPath = vfs.CleanRemote(oldPath), vfs.CleanRemote(newPath)
	// pkg/sftp's Rename already fails on an existing target, but check first
	// so the error is the same "already exists" every backend returns rather
	// than a raw SFTP status string.
	if _, err := f.client.Lstat(newPath); err == nil {
		return fmt.Errorf("%w: %s", vfs.ErrExists, newPath)
	}
	return f.client.Rename(oldPath, newPath)
}

func (f *FS) Remove(ctx context.Context, targetPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	targetPath = vfs.CleanRemote(targetPath)
	if err := f.client.Remove(targetPath); err == nil {
		return nil
	}
	return f.client.RemoveDirectory(targetPath)
}

func (f *FS) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := f.client.Open(vfs.CleanRemote(path))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func (f *FS) WriteFile(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := f.client.Create(vfs.CleanRemote(path))
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
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
