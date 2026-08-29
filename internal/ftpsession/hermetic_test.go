package ftpsession

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tideftp/internal/domain"
	"tideftp/internal/transfer"
)

func ftpCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestFSListReturnsServerEntries(t *testing.T) {
	server := startFTPServer(t)
	server.writeFile(t, "alpha.txt", []byte("a"))
	server.writeFile(t, "beta.txt", []byte("bb"))
	if err := os.Mkdir(server.path("sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	fs := server.connect(t).FS()

	entries, err := fs.List(ftpCtx(t), "/", true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := names(entries)
	for _, want := range []string{"alpha.txt", "beta.txt", "sub"} {
		if !contains(got, want) {
			t.Fatalf("List = %v, want it to include %q", got, want)
		}
	}
	if contains(got, ".") || contains(got, "..") {
		t.Fatalf("List = %v, want . and .. dropped", got)
	}
}

func TestFSMkdirCreatesDirectory(t *testing.T) {
	server := startFTPServer(t)
	fs := server.connect(t).FS()

	if err := fs.Mkdir(ftpCtx(t), "/reports"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	info, err := os.Stat(server.path("reports"))
	if err != nil || !info.IsDir() {
		t.Fatalf("Stat(reports) = %v, %v; want a directory", info, err)
	}
}

func TestFSRenameMovesEntry(t *testing.T) {
	server := startFTPServer(t)
	server.writeFile(t, "before.txt", []byte("payload"))
	fs := server.connect(t).FS()

	if err := fs.Rename(ftpCtx(t), "/before.txt", "/after.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := os.Stat(server.path("before.txt")); !os.IsNotExist(err) {
		t.Fatalf("old name still present, Stat err = %v", err)
	}
	body, err := os.ReadFile(server.path("after.txt"))
	if err != nil || string(body) != "payload" {
		t.Fatalf("after.txt = %q, %v; want \"payload\"", body, err)
	}
}

func TestFSRenameRefusesExistingTarget(t *testing.T) {
	server := startFTPServer(t)
	server.writeFile(t, "keep.txt", []byte("keep"))
	server.writeFile(t, "victim.txt", []byte("victim"))
	fs := server.connect(t).FS()

	err := fs.Rename(ftpCtx(t), "/keep.txt", "/victim.txt")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Rename onto an existing name = %v, want an \"already exists\" error", err)
	}
	if body, _ := os.ReadFile(server.path("victim.txt")); string(body) != "victim" {
		t.Fatalf("victim.txt = %q, want it untouched", body)
	}
	if _, err := os.Stat(server.path("keep.txt")); err != nil {
		t.Fatalf("keep.txt should still be there: %v", err)
	}
}

func TestFSRemoveDeletesFilesAndEmptyDirs(t *testing.T) {
	server := startFTPServer(t)
	server.writeFile(t, "gone.txt", []byte("x"))
	if err := os.Mkdir(server.path("emptydir"), 0o755); err != nil {
		t.Fatal(err)
	}
	fs := server.connect(t).FS()
	ctx := ftpCtx(t)

	if err := fs.Remove(ctx, "/gone.txt"); err != nil {
		t.Fatalf("Remove file: %v", err)
	}
	if _, err := os.Stat(server.path("gone.txt")); !os.IsNotExist(err) {
		t.Fatalf("file not removed, Stat err = %v", err)
	}

	// Remove tries Delete first; for a directory that fails and it falls back
	// to RemoveDir.
	if err := fs.Remove(ctx, "/emptydir"); err != nil {
		t.Fatalf("Remove dir: %v", err)
	}
	if _, err := os.Stat(server.path("emptydir")); !os.IsNotExist(err) {
		t.Fatalf("dir not removed, Stat err = %v", err)
	}
}

func TestEngineUploadAndDownloadRoundTrip(t *testing.T) {
	server := startFTPServer(t)
	conn := server.connect(t)
	engine := conn.Engine()

	body := bytes.Repeat([]byte("tideftp ftp round trip\n"), 4000)
	local := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(local, body, 0o644); err != nil {
		t.Fatal(err)
	}

	engine.Start(transfer.Request{
		ID: 1, Direction: domain.Upload,
		Source: local, Destination: "/payload.bin", Size: int64(len(body)),
	})
	if event := awaitTerminal(t, engine, 1); event.Kind != transfer.Completed {
		t.Fatalf("upload terminal event = %v (err %v), want Completed", event.Kind, event.Err)
	}
	if got, err := os.ReadFile(server.path("payload.bin")); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("uploaded file: %d bytes on server, %d sent (err %v)", len(got), len(body), err)
	}

	back := filepath.Join(t.TempDir(), "back.bin")
	engine.Start(transfer.Request{
		ID: 2, Direction: domain.Download,
		Source: "/payload.bin", Destination: back, Size: int64(len(body)),
	})
	if event := awaitTerminal(t, engine, 2); event.Kind != transfer.Completed {
		t.Fatalf("download terminal event = %v (err %v), want Completed", event.Kind, event.Err)
	}
	if got, err := os.ReadFile(back); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("downloaded file: %d bytes locally, %d on server (err %v)", len(got), len(body), err)
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
