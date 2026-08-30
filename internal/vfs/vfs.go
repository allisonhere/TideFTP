// Package vfs defines the filesystem interface the file panes browse through.
// Both panes use it: the local pane over real disk (internal/localfs) and the
// remote pane over a protocol adapter (internal/fakefs today, FTP/FTPS/SFTP
// later).
//
// List blocks and takes a context. Implementations talk to a real server or a
// real disk and both can hang, so callers must run it off whatever goroutine
// has to stay responsive — internal/ui wraps it in a tea.Cmd. Unlike
// transfer.Engine, which streams progress over a channel because a transfer is
// long-lived, a listing is one-shot request/response, so a plain blocking call
// is the right shape. Keeping it free of any UI framework also leaves the
// adapters usable from the planned non-interactive CLI mode.
package vfs

import (
	"context"
	"errors"
	"io"
	"path"
	"strings"

	"tideftp/internal/domain"
)

// ErrExists is wrapped by Mkdir and Rename when the destination path is
// already taken. The UI checks for it with errors.Is to offer an overwrite
// confirmation rather than just surfacing the message.
var ErrExists = errors.New("already exists")

type FS interface {
	// List returns the entries in dirPath, omitting hidden ones unless
	// showHidden is set. It must honour ctx cancellation.
	List(ctx context.Context, dirPath string, showHidden bool) ([]domain.Entry, error)
	// Child resolves the path of name inside current. It is pure path math:
	// no I/O, no error, safe to call on the UI goroutine.
	Child(current, name string) string
	// Parent resolves the parent of current, returning current unchanged at
	// the root. Pure path math, like Child.
	Parent(current string) string
	// Mkdir creates one directory at dirPath.
	Mkdir(ctx context.Context, dirPath string) error
	// Rename moves oldPath to newPath.
	Rename(ctx context.Context, oldPath, newPath string) error
	// Remove deletes the file or empty directory at targetPath.
	Remove(ctx context.Context, targetPath string) error
	// ReadFile returns the whole contents of the file at path. Callers keep
	// it to small files (the edit flow caps the size itself).
	ReadFile(ctx context.Context, path string) ([]byte, error)
	// Open returns a reader over the file at path, which the caller must
	// Close. Unlike ReadFile it never buffers the whole file, which is the
	// point: the preview flow reads only the first few KB of a file that
	// may be gigabytes, and the checksum verify flow streams every byte
	// through a hash without ever holding more than a chunk of it. ctx
	// bounds opening the file; the returned reader's own lifetime is the
	// caller's to manage.
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	// WriteFile replaces the contents of the file at path, creating it if it
	// does not exist. An existing file keeps its permissions.
	WriteFile(ctx context.Context, path string, data []byte) error
}

// Remote paths are always slash-separated, whatever the client's own OS is,
// so these use path rather than filepath. Both remote adapters share them so
// their navigation cannot drift apart.

// CleanRemote normalises a remote path to an absolute POSIX one.
func CleanRemote(value string) string {
	if strings.TrimSpace(value) == "" {
		return "/"
	}
	return path.Clean("/" + strings.TrimPrefix(value, "/"))
}

// ChildRemote resolves name inside current.
func ChildRemote(current, name string) string {
	return CleanRemote(path.Join(CleanRemote(current), name))
}

// ParentRemote resolves the parent of current, returning "/" at the root so
// the UI knows there is nowhere further up to go.
func ParentRemote(current string) string {
	parent := path.Dir(CleanRemote(current))
	if parent == "." {
		return "/"
	}
	return parent
}
