// Package netcheck answers one cheap question with no protocol knowledge: can
// this machine actually reach the server a transfer is failing against?
//
// It exists because a transfer failure is ambiguous. "Permission denied" and
// "the Wi-Fi is gone" both arrive as an error on one file, and firing the rest
// of the queue at a dead link just multiplies the noise. A short TCP dial —
// no SSH or FTP handshake — is enough to tell the two apart, and it works for
// every protocol the app speaks.
package netcheck

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"
)

// Status is the outcome of a reachability probe.
type Status int

const (
	// Reachable means a TCP connection to the server succeeded.
	Reachable Status = iota
	// HostUnreachable means this machine has a link, but the server did not
	// answer.
	HostUnreachable
	// NoLink means this machine has no usable network interface at all, so
	// the server could not have been reached regardless.
	NoLink
)

// Result is a probe's verdict plus a short phrase ready for a status line.
type Result struct {
	Status Status
	// Detail is a human-readable one-liner, not an error dump.
	Detail string
}

// DefaultTimeout bounds one dial. It is short on purpose: this runs while the
// user is watching a failed transfer, and a reachable server answers long
// before it.
const DefaultTimeout = 3 * time.Second

// Probe checks whether host:port is reachable, first asking whether this
// machine even has a network link. A missing link is reported distinctly so
// the UI can say "no network connection" rather than blaming the server.
func Probe(ctx context.Context, host string, port int, timeout time.Duration) Result {
	return probe(ctx, host, port, timeout, localLinkUp)
}

// probe is Probe with the link check injected, so tests can exercise both the
// link and dial branches without depending on the machine's interfaces.
func probe(ctx context.Context, host string, port int, timeout time.Duration, linkUp func() bool) Result {
	if !linkUp() {
		return Result{Status: NoLink, Detail: "no network connection"}
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	address := net.JoinHostPort(host, strconv.Itoa(port))
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return Result{Status: HostUnreachable, Detail: "server unreachable"}
	}
	_ = conn.Close()
	return Result{Status: Reachable, Detail: "server reachable"}
}

// localLinkUp reports whether any non-loopback interface is up with a usable
// unicast address. It deliberately returns true when the interfaces cannot be
// listed: not knowing is not evidence of an outage, and a false "no network"
// is worse than a probe that falls through to the dial.
func localLinkUp() bool {
	interfaces, err := net.Interfaces()
	if err != nil {
		return true
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ipnet, ok := address.(*net.IPNet)
			if ok && ipnet.IP.IsGlobalUnicast() {
				return true
			}
		}
	}
	return false
}

// String names the status for logs and test failures.
func (s Status) String() string {
	switch s {
	case Reachable:
		return "reachable"
	case HostUnreachable:
		return "host unreachable"
	case NoLink:
		return "no link"
	default:
		return fmt.Sprintf("status(%d)", int(s))
	}
}
