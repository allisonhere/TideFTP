package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
	"tideftp/internal/localfs"
	"tideftp/internal/transfer"
	"tideftp/internal/vfs"
)

// verifyModel wires up a connected model with checksum verification on and
// one Active transfer of local file src to remote path dst, ready for the
// engine to report it complete.
func verifyModel(t *testing.T, remote vfs.FS, srcBody string) (Model, string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "payload.txt")
	if err := os.WriteFile(src, []byte(srcBody), 0o644); err != nil {
		t.Fatal(err)
	}
	model := loadedModelOver(t, localfs.New(), remote, newScriptedEngine())
	model.verifyChecksums = true
	model.transfers = []domain.Transfer{{
		ID:          1,
		Direction:   domain.Upload,
		Source:      src,
		Destination: "/public_html/robots.txt",
		BytesTotal:  int64(len(srcBody)),
		Status:      domain.Active,
	}}
	return model, src
}

// completeTransfer feeds the engine's Completed event in and settles the
// verification it kicks off.
func completeTransfer(t *testing.T, model Model, id int, size int64) Model {
	t.Helper()
	updated, cmd := model.Update(transfer.Event{ID: id, Kind: transfer.Completed, BytesDone: size})
	return settle(t, updated.(Model), cmd)
}

func TestVerifyMarksAMatchingTransferVerified(t *testing.T) {
	remote := fakefs.NewRemote()
	body, err := remote.ReadFile(t.Context(), "/public_html/robots.txt")
	if err != nil {
		t.Fatal(err)
	}
	model, _ := verifyModel(t, remote, string(body))

	model = completeTransfer(t, model, 1, int64(len(body)))

	if model.transfers[0].Status != domain.Done {
		t.Fatalf("status = %v, want a verified transfer to stay Done", model.transfers[0].Status)
	}
	if model.transfers[0].Message != "verified" {
		t.Fatalf("message = %q, want %q", model.transfers[0].Message, "verified")
	}
}

func TestVerifyFailsATransferWhoseChecksumDiffers(t *testing.T) {
	model, _ := verifyModel(t, fakefs.NewRemote(), "not what is on the server")

	model = completeTransfer(t, model, 1, 25)

	if model.transfers[0].Status != domain.Failed {
		t.Fatalf("status = %v, want a mismatch to fail the transfer", model.transfers[0].Status)
	}
	if model.transfers[0].Message != "checksum mismatch" {
		t.Fatalf("message = %q, want %q", model.transfers[0].Message, "checksum mismatch")
	}
	if !model.statusErr {
		t.Fatalf("a checksum mismatch must be reported as an error, status=%q", model.status)
	}
}

// unreadableFS refuses Open, standing in for a destination that cannot be
// read back — a dropped connection, a server that will not serve the file it
// just accepted.
type unreadableFS struct{ vfs.FS }

func (f *unreadableFS) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("connection reset by peer")
}

func TestVerifyThatCannotRunLeavesTheTransferDone(t *testing.T) {
	model, _ := verifyModel(t, &unreadableFS{FS: fakefs.NewRemote()}, "payload")

	model = completeTransfer(t, model, 1, 7)

	if model.transfers[0].Status != domain.Done {
		t.Fatalf("status = %v: failing to check is not the same as finding a difference", model.transfers[0].Status)
	}
	if model.transfers[0].Message != "unverified" {
		t.Fatalf("message = %q, want %q", model.transfers[0].Message, "unverified")
	}
	if !strings.Contains(model.status, "verify transfer 1") {
		t.Fatalf("status = %q, want it to name the verification that could not run", model.status)
	}
}

func TestVerifyIsSkippedWhenTheSettingIsOff(t *testing.T) {
	model, _ := verifyModel(t, fakefs.NewRemote(), "anything at all")
	model.verifyChecksums = false

	model = completeTransfer(t, model, 1, 15)

	if model.transfers[0].Status != domain.Done || model.transfers[0].Message != "complete" {
		t.Fatalf("row = %+v, want the untouched complete row verification is off for", model.transfers[0])
	}
}

func TestVerifyTimeoutScalesWithTheFile(t *testing.T) {
	small, large := verifyTimeout(1024), verifyTimeout(4<<30)
	if small != 60e9 {
		t.Fatalf("small file timeout = %v, want the 60s floor", small)
	}
	if large <= small {
		t.Fatalf("large file timeout = %v, want more than the floor %v", large, small)
	}
	if capped := verifyTimeout(1 << 50); capped != 2*60*60e9 {
		t.Fatalf("timeout = %v for an absurd size, want it capped at 2h", capped)
	}
}
