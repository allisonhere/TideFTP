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

	// IdentityFile is SFTP-specific: it overrides the Dialer's configured
	// identity files with this single path, and implies not offering the
	// agent — the same as the --identity flag does at startup, but for one
	// attempt. Empty defers to however the Dialer was already configured.
	IdentityFile string
	// KnownHostsPath is SFTP-specific: it overrides the Dialer's configured
	// known_hosts file for this attempt. Empty defers to however the Dialer
	// was already configured.
	KnownHostsPath string

	// FTPSCAFile is FTPS-specific: a PEM file to trust for this attempt,
	// overriding the Dialer's own configured one. Empty defers to however
	// the Dialer was already configured.
	FTPSCAFile string
	// FTPSInsecure is FTPS-specific: it accepts any server certificate for
	// this attempt. It only ever turns verification off, never back on —
	// ORed with the Dialer's own configured setting rather than replacing
	// it, so this cannot silently make a Dialer already configured insecure
	// look verified.
	FTPSInsecure bool

	// TrustedHostKey is SFTP-specific: the exact marshaled bytes of a host
	// key the user has just agreed to trust, after Dial returned an
	// UntrustedHostKeyError naming it. Only a key matching these bytes
	// exactly is accepted — this can never widen trust beyond the one key
	// that was actually shown and approved, and never overrides a real
	// mismatch against an already-known host. Empty means nothing has been
	// pre-approved for this attempt.
	TrustedHostKey []byte
	// RememberHostKey persists TrustedHostKey to the known_hosts file once
	// the connection succeeds, so future attempts see it as an ordinary
	// known host instead of asking again. Ignored when TrustedHostKey is
	// empty.
	RememberHostKey bool
}

// UntrustedHostKeyError is returned by Dial when a server's identity is not
// yet trusted but could be, once the user confirms it — an unknown SSH host
// key. It never fires for a host whose key does not match what's already
// pinned (that's a straight failure, always). SFTP is the only Dialer that
// raises this today; it lives here, not in internal/sftpsession, because the
// UI branches on it and must never import a protocol-specific adapter.
type UntrustedHostKeyError struct {
	Address     string // host:port that was dialed
	Algorithm   string // e.g. "ssh-ed25519"
	Fingerprint string // e.g. "SHA256:...", ready to show the user
	Key         []byte // ssh.PublicKey.Marshal() of the offered key, opaque here
}

func (e *UntrustedHostKeyError) Error() string {
	return fmt.Sprintf("unknown host key for %s: %s %s", e.Address, e.Algorithm, e.Fingerprint)
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
