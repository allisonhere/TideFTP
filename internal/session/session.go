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
	// HostKeyPolicy is SFTP-only and one of "", "strict", or "off" ("" means
	// ask). "strict" fails on any host not already in known_hosts, with no
	// prompt; "off" skips host-key verification entirely. Empty is the
	// default ask-once-then-remember behaviour. It persists with the profile.
	HostKeyPolicy string
	// Bookmarks are absolute directories on this server the remote pane can
	// jump to, beyond StartPath. They persist with the profile.
	Bookmarks []string
}

// SameConnection reports whether two targets describe the same connection.
//
// Target holds a slice, so it is not comparable with == and callers that used
// to compare it that way use this instead. Bookmarks are deliberately not part
// of the answer: they are what the user saved *about* a server, not part of
// which server it is, and a dial result must still match the target it was
// dialled for after a bookmark is added mid-connect.
func (t Target) SameConnection(other Target) bool {
	return t.Name == other.Name &&
		t.Protocol == other.Protocol &&
		t.Host == other.Host &&
		t.Port == other.Port &&
		t.User == other.User &&
		t.StartPath == other.StartPath &&
		t.HostKeyPolicy == other.HostKeyPolicy
}

// Host-key verification policies for Target.HostKeyPolicy.
const (
	HostKeyAsk    = "" // prompt once for an unknown host, then remember
	HostKeyStrict = "strict"
	HostKeyOff    = "off"
)

// NormalizeHostKeyPolicy maps any unrecognised value to the ask default.
func NormalizeHostKeyPolicy(value string) string {
	switch value {
	case HostKeyStrict, HostKeyOff:
		return value
	default:
		return HostKeyAsk
	}
}

// The protocols a Target can name. FTPS comes in two incompatible flavours
// and they are separate protocols here rather than a flag on one, because
// everything that dispatches on Protocol — the router, the port default, the
// connect form — has to tell them apart anyway.
//
// ProtocolFTPS is explicit FTPS: an ordinary FTP connection on the FTP port
// that AUTH TLS upgrades in place. ProtocolFTPSImplicit is implicit FTPS: the
// TLS handshake happens first, before any FTP command, on a port dedicated to
// it. A server speaking one will not answer the other.
const (
	ProtocolSFTP         = "sftp"
	ProtocolFTP          = "ftp"
	ProtocolFTPS         = "ftps"
	ProtocolFTPSImplicit = "ftps-implicit"
)

// IsFTPS reports whether protocol is one of the two FTPS flavours. The TLS
// settings — the CA file, the verify toggle — apply to both, so callers that
// gate on "does this connection use TLS?" ask this rather than comparing
// against ProtocolFTPS and missing the implicit one.
func IsFTPS(protocol string) bool {
	return protocol == ProtocolFTPS || protocol == ProtocolFTPSImplicit
}

// DefaultPort is the port for a protocol when a Target does not name one.
//
// Explicit FTPS defaults to 21, not 990: it begins as a plain FTP connection
// and only upgrades once AUTH TLS is accepted, so it belongs on the FTP port.
// 990 is implicit FTPS's port, where the server expects a TLS handshake
// immediately — offering AUTH TLS there reaches a server waiting for a
// ClientHello, and the connection hangs or resets rather than failing
// informatively.
func DefaultPort(protocol string) int {
	switch protocol {
	case ProtocolFTP, ProtocolFTPS:
		return 21
	case ProtocolFTPSImplicit:
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
	// KeyPassphrase is SFTP-specific: the passphrase for an encrypted private
	// key. It is only consulted when parsing a key file that turns out to be
	// passphrase-protected, and is never stored — the connect form prompts
	// for it each attempt.
	KeyPassphrase string
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
