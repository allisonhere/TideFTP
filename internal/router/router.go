// Package router multiplexes session.Dialer across protocols.
//
// Without it, the app could only ever hold one concrete Dialer — whichever
// protocol was chosen at startup — while the connect form lets the user pick
// sftp/ftp/ftps per attempt. Switching Protocol in the form and connecting
// would silently dial the new target through the old adapter: an FTP target
// handed to the SFTP client, say, producing a confusing handshake failure
// instead of using the right one. Dialer here is what the form's Protocol
// field actually reaches: it holds one real Dialer per protocol and picks
// between them by the target it is asked to dial.
package router

import (
	"context"
	"fmt"

	"tideftp/internal/session"
)

var _ session.Dialer = (*Dialer)(nil)

// Dialer picks a protocol-specific session.Dialer by Target.Protocol.
type Dialer struct {
	byProtocol map[string]session.Dialer
}

// New builds a Dialer over byProtocol, keyed by the protocol string a Target
// names ("sftp", "ftp", "ftps"). A protocol with no entry fails Dial with a
// clear error rather than falling back to some other adapter.
func New(byProtocol map[string]session.Dialer) *Dialer {
	return &Dialer{byProtocol: byProtocol}
}

func (d *Dialer) Dial(ctx context.Context, target session.Target, creds session.Credentials) (session.Conn, error) {
	dialer, ok := d.byProtocol[target.Protocol]
	if !ok {
		return nil, fmt.Errorf("no dialer configured for protocol %q", target.Protocol)
	}
	return dialer.Dial(ctx, target, creds)
}
