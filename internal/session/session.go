// Package session models the lifecycle of a connection to a server.
//
// The adapter seams below it — vfs.FS for browsing, transfer.Engine for moving
// bytes — both assume they are already connected, which is true of the fakes
// and false of anything real. A real client connects after the user fills in a
// form, drops without warning, and has to be reconnected. Conn is where that
// lives: it hands out an FS and an Engine that are valid only while the
// connection is, and reports the end on Done.
//
// Nothing here knows about a UI framework. internal/ui wraps Dial in a tea.Cmd
// the same way it wraps vfs.FS.List.
package session

import (
	"context"
	"fmt"

	"tideftp/internal/transfer"
	"tideftp/internal/vfs"
)

// Target is where to connect and as whom. It is the part a saved profile will
// eventually persist; credentials deliberately live elsewhere.
type Target struct {
	Name      string
	Protocol  string
	Host      string
	Port      int
	User      string
	StartPath string
}

// DefaultPort is the port for a protocol when a Target does not name one.
func DefaultPort(protocol string) int {
	switch protocol {
	case "ftp":
		return 21
	case "ftps":
		return 990
	default:
		return 22
	}
}

// Address is the host:port to dial.
func (t Target) Address() string {
	port := t.Port
	if port == 0 {
		port = DefaultPort(t.Protocol)
	}
	return fmt.Sprintf("%s:%d", t.Host, port)
}

// Label is a short human-readable identity for status bars and menus.
func (t Target) Label() string {
	if t.Name != "" {
		return t.Name
	}
	if t.User == "" {
		return fmt.Sprintf("%s (%s)", t.Host, t.Protocol)
	}
	return fmt.Sprintf("%s@%s (%s)", t.User, t.Host, t.Protocol)
}

// Home is the directory to open on connect.
func (t Target) Home() string {
	if t.StartPath == "" {
		return "/"
	}
	return t.StartPath
}

// Credentials authenticates one Dial attempt. Unlike Target, it is never
// persisted — a saved profile keeps where to connect and as whom, never how
// to prove it.
type Credentials struct {
	// Password authenticates the connection. FTP and FTPS always need one;
	// SFTP only uses it when PasswordOnly is set. Empty defers to however the
	// Dialer was already configured (an env var, typically).
	Password string
	// PasswordOnly is SFTP-specific: it forces password authentication and
	// skips the SSH agent and key files entirely, rather than trying Password
	// only as a fallback after they fail. FTP and FTPS ignore it, since they
	// have no other auth method to skip.
	PasswordOnly bool
}

// Conn is a live connection. Its FS and Engine are valid only until the
// connection ends; callers must stop using them once Done fires.
type Conn interface {
	// FS browses the server. Never nil for a live Conn.
	FS() vfs.FS
	// Engine moves bytes over this connection. Never nil for a live Conn.
	Engine() transfer.Engine
	// Done receives exactly one value when the connection ends, then closes:
	// nil if Close was called, otherwise the reason it dropped.
	Done() <-chan error
	// Close ends the connection. It is safe to call more than once, and safe
	// to call after the connection has already dropped.
	Close() error
}

// Dialer opens connections. Implementations are protocol-specific; the UI
// holds one and knows nothing about what it dials.
type Dialer interface {
	// Dial connects to target as creds, honouring ctx for the connect attempt
	// only. Cancelling ctx afterwards does not close the returned Conn. A
	// Dialer that authenticates itself (the demo fakes) ignores creds.
	Dial(ctx context.Context, target Target, creds Credentials) (Conn, error)
}
