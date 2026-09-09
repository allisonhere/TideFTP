package sftpsession

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"tideftp/internal/domain"
	"tideftp/internal/transfer"
)

// stalledFile lets the SSH connection remain alive while one SFTP request
// never receives a response. Tests release it only during cleanup.
type stalledFile struct {
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
	stallOpen bool
}

func (f *stalledFile) stall() {
	f.once.Do(func() { close(f.entered) })
	<-f.release
}

func (f *stalledFile) Fileread(*sftp.Request) (io.ReaderAt, error) {
	if f.stallOpen {
		f.stall()
	}
	return f, nil
}

func (f *stalledFile) Filewrite(*sftp.Request) (io.WriterAt, error) {
	return f, nil
}

func (f *stalledFile) ReadAt([]byte, int64) (int, error) {
	f.stall()
	return 0, io.EOF
}

func (f *stalledFile) WriteAt(p []byte, _ int64) (int, error) {
	f.stall()
	return len(p), nil
}

func TestStalledTransferCanStop(t *testing.T) {
	for _, operation := range []string{"read", "write", "open"} {
		for _, action := range []string{"cancel", "disconnect", "engine close"} {
			t.Run(operation+"/"+action, func(t *testing.T) {
				file := &stalledFile{entered: make(chan struct{}), release: make(chan struct{}), stallOpen: operation == "open"}
				server := startTestServerWithHandlers(t, &sftp.Handlers{FileGet: file, FilePut: file})
				conn := connect(t, server)
				t.Cleanup(func() { close(file.release) })
				local := filepath.Join(t.TempDir(), "data")
				req := transfer.Request{ID: 1, Direction: domain.Download, Source: "data", Destination: local, Size: 4}
				if operation == "write" {
					if err := os.WriteFile(local, []byte("data"), 0o600); err != nil {
						t.Fatal(err)
					}
					req.Direction, req.Source, req.Destination = domain.Upload, local, "data"
				}
				conn.Engine().Start(req)
				select {
				case <-file.entered:
				case <-time.After(5 * time.Second):
					t.Fatal("transfer did not reach stalled request")
				}
				if action == "cancel" {
					conn.Engine().Cancel(1)
					select {
					case event := <-conn.Engine().Events():
						if event.Kind != transfer.Canceled {
							t.Fatalf("event = %+v, want Canceled", event)
						}
					case <-time.After(3 * time.Second):
						t.Fatal("cancel hung on stalled SFTP request")
					}
				} else {
					done := make(chan struct{})
					go func() {
						defer close(done)
						if action == "disconnect" {
							_ = conn.Close()
						} else {
							_ = conn.Engine().Close()
						}
					}()
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Fatal("close hung on stalled SFTP request")
					}
				}
			})
		}
	}
}
