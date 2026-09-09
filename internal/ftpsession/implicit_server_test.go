package ftpsession

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// This file is an implicit-FTPS server, hand-rolled because the in-process
// server the rest of these tests use — goftp.io/server/v2 — cannot act as one.
// Its ListenAndServe does have a tls.Listen branch, but a session only sets
// its internal tls flag from the AUTH TLS upgrade path, so on an
// implicitly-wrapped listener it still answers PBSZ and PROT with 550 and the
// client's login fails. Rather than skip the mode entirely, this speaks the
// small slice of FTP that dialing and listing actually exercise.
//
// It is deliberately minimal: implicit FTPS differs from explicit only in when
// the handshake happens, and everything afterwards is the plain-FTP path the
// other tests already cover against the real server. What needs proving here
// is that the client handshakes before saying anything, and that the
// certificate is verified.

const implicitTestListing = "-rw-r--r-- 1 owner group 5 Jan 02 15:04 notes.txt\r\n" +
	"drwxr-xr-x 2 owner group 4096 Jan 02 15:04 reports\r\n"

type implicitServer struct {
	addr     string
	caFile   string
	listener net.Listener

	mu sync.Mutex
	// plaintextBytes records anything a client sent before completing a TLS
	// handshake. It must stay empty: leaking FTP commands in the clear is
	// precisely the failure implicit FTPS exists to prevent.
	plaintextBytes int
}

// startImplicitServer listens with TLS already wrapped around the socket, so
// no greeting is sent until a client has completed a handshake.
func startImplicitServer(t *testing.T) *implicitServer {
	t.Helper()

	cert, certPEM := selfSignedCert(t)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatalf("write CA file: %v", err)
	}
	config := &tls.Config{Certificates: []tls.Certificate{cert}}

	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &implicitServer{addr: raw.Addr().String(), caFile: caFile, listener: raw}
	t.Cleanup(func() { _ = raw.Close() })

	go func() {
		for {
			conn, err := raw.Accept()
			if err != nil {
				return
			}
			go s.serve(conn, config)
		}
	}()
	return s
}

func (s *implicitServer) serve(raw net.Conn, config *tls.Config) {
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(30 * time.Second))

	// Peek at the first byte before handing the connection to TLS. A
	// ClientHello starts with 0x16 (handshake); anything else is a client
	// that spoke FTP in the clear, which is the bug this guards against.
	peeked := bufio.NewReader(raw)
	first, err := peeked.Peek(1)
	if err != nil {
		return
	}
	if first[0] != 0x16 {
		s.mu.Lock()
		s.plaintextBytes++
		s.mu.Unlock()
		return
	}

	conn := tls.Server(readerConn{Conn: raw, r: peeked}, config)
	if err := conn.Handshake(); err != nil {
		return
	}
	defer conn.Close()

	w := bufio.NewWriter(conn)
	reply := func(format string, args ...any) {
		fmt.Fprintf(w, format+"\r\n", args...)
		_ = w.Flush()
	}
	reply("220 implicit FTPS ready")

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "FEAT":
			reply("211-Features:\r\n PBSZ\r\n PROT\r\n UTF8\r\n211 End")
		case "USER":
			reply("331 password required")
		case "PASS":
			if arg == ftpTestPass {
				reply("230 logged in")
			} else {
				reply("530 login incorrect")
			}
		case "PBSZ":
			reply("200 OK")
		case "PROT":
			reply("200 OK")
		case "OPTS":
			reply("200 OK")
		case "TYPE":
			reply("200 OK")
		case "SYST":
			reply("215 UNIX Type: L8")
		case "PWD":
			reply("257 \"/\"")
		case "CWD":
			reply("250 OK")
		case "EPSV":
			// Refused so the client falls back to PASV, whose reply format
			// this server can produce without guessing at extended syntax.
			reply("500 not supported")
		case "PASV":
			port, err := s.openDataConnection(config)
			if err != nil {
				reply("425 cannot open data connection")
				continue
			}
			reply("227 Entering Passive Mode (127,0,0,1,%d,%d)", port>>8, port&0xff)
		case "LIST", "NLST", "MLSD":
			reply("150 opening data connection")
			reply("226 transfer complete")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 not implemented")
		}
	}
}

// openDataConnection listens on an ephemeral port for one passive data
// connection, writes the canned listing over TLS, and closes. The data
// connection is TLS too — after PROT P, which the client always sends here.
func (s *implicitServer) openDataConnection(config *tls.Config) (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	go func() {
		defer listener.Close()
		_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(15 * time.Second))
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		secure := tls.Server(conn, config)
		if err := secure.Handshake(); err != nil {
			return
		}
		_, _ = secure.Write([]byte(implicitTestListing))
		_ = secure.Close()
	}()
	return port, nil
}

// sawPlaintext reports whether any client ever sent something other than a
// TLS ClientHello as its first bytes.
func (s *implicitServer) sawPlaintext() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.plaintextBytes > 0
}

// readerConn lets tls.Server consume the byte already peeked off the socket.
type readerConn struct {
	net.Conn
	r *bufio.Reader
}

func (c readerConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// selfSignedCert mints a throwaway certificate valid for 127.0.0.1.
func selfSignedCert(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, certPEM
}
