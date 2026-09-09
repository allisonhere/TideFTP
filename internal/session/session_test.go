package session

import "testing"

// TestDefaultPortFTPSFlavours pins the distinction the two FTPS protocols
// exist for. Explicit FTPS starts as plain FTP on 21 and upgrades with AUTH
// TLS; implicit FTPS expects a TLS handshake immediately, on 990. Defaulting
// explicit FTPS to 990 — as this once did — points an AUTH TLS dialer at a
// server waiting for a ClientHello, which stalls rather than failing clearly.
func TestDefaultPortFTPSFlavours(t *testing.T) {
	for _, tc := range []struct {
		protocol string
		want     int
	}{
		{ProtocolSFTP, 22},
		{ProtocolFTP, 21},
		{ProtocolFTPS, 21},
		{ProtocolFTPSImplicit, 990},
		{"", 22},
		{"nonsense", 22},
	} {
		if got := DefaultPort(tc.protocol); got != tc.want {
			t.Errorf("DefaultPort(%q) = %d, want %d", tc.protocol, got, tc.want)
		}
	}
}

func TestAddressUsesProtocolDefaultPort(t *testing.T) {
	explicit := Target{Protocol: ProtocolFTPS, Host: "example.com"}
	if got, want := explicit.Address(), "example.com:21"; got != want {
		t.Errorf("explicit FTPS Address() = %q, want %q", got, want)
	}
	implicit := Target{Protocol: ProtocolFTPSImplicit, Host: "example.com"}
	if got, want := implicit.Address(), "example.com:990"; got != want {
		t.Errorf("implicit FTPS Address() = %q, want %q", got, want)
	}
	// An explicit port always wins over the protocol's default.
	pinned := Target{Protocol: ProtocolFTPSImplicit, Host: "example.com", Port: 2121}
	if got, want := pinned.Address(), "example.com:2121"; got != want {
		t.Errorf("pinned Address() = %q, want %q", got, want)
	}
}

// TestIsFTPS guards the reason the helper exists: callers gating TLS settings
// must not compare against ProtocolFTPS alone and silently skip the implicit
// flavour, which needs the same CA file and verify toggle.
func TestIsFTPS(t *testing.T) {
	for _, tc := range []struct {
		protocol string
		want     bool
	}{
		{ProtocolFTPS, true},
		{ProtocolFTPSImplicit, true},
		{ProtocolFTP, false},
		{ProtocolSFTP, false},
		{"", false},
	} {
		if got := IsFTPS(tc.protocol); got != tc.want {
			t.Errorf("IsFTPS(%q) = %v, want %v", tc.protocol, got, tc.want)
		}
	}
}
