package ftpsession

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"tideftp/internal/session"
)

// startBreakingFTPServer is a minimal scripted FTP server whose RETR sends a
// few bytes, drops the data connection, and then reports the transfer as
// aborted with 426 — what a server says when a download breaks partway.
func startBreakingFTPServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveBreakingFTP(c)
		}
	}()
	return ln.Addr().String()
}

func serveBreakingFTP(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	reply := func(s string) { fmt.Fprintf(c, "%s\r\n", s) }
	reply("220 ready")
	var data net.Listener
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.Fields(strings.TrimSpace(line) + " x")[0])
		switch cmd {
		case "USER":
			reply("331 password please")
		case "PASS":
			reply("230 logged in")
		case "FEAT":
			reply("502 no features")
		case "TYPE":
			reply("200 binary")
		case "EPSV":
			if data, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
				reply("425 no data")
				continue
			}
			reply(fmt.Sprintf("229 Entering Extended Passive Mode (|||%d|)", data.Addr().(*net.TCPAddr).Port))
		case "RETR":
			reply("150 opening data connection")
			if dc, err := data.Accept(); err == nil {
				_, _ = dc.Write([]byte("first half of the fi"))
				_ = dc.Close()
			}
			_ = data.Close()
			reply("426 Connection closed; transfer aborted")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("200 ok")
		}
	}
}

// A download that breaks partway must not come back as a complete file: the
// edit flow would otherwise write the truncated copy back over the original.
func TestFSReadFileReportsATransferTheServerAborted(t *testing.T) {
	addr := startBreakingFTPServer(t)
	host, portText, _ := net.SplitHostPort(addr)
	var port int
	fmt.Sscan(portText, &port)
	conn, err := New(Config{Timeout: 5 * time.Second}).Dial(context.Background(),
		session.Target{Protocol: "ftp", Host: host, Port: port, User: "u"}, session.Credentials{Password: "p"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := conn.FS().ReadFile(ctx, "/app.conf")
	if err == nil {
		t.Fatalf("ReadFile = %q with no error, want the 426 reported", data)
	}
	if data != nil {
		t.Fatalf("ReadFile returned %d partial bytes alongside the error", len(data))
	}
}
