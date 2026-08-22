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

	"tideftp/internal/domain"
)

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
}
