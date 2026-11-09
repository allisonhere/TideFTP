package ui

import (
	"strings"
	"testing"

	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
	"tideftp/internal/session"
)

// otherServer is a connection that is not the one loadedModelWithDialer
// connects to.
var otherServer = session.Target{Name: "other", Protocol: "sftp", Host: "elsewhere.example.com", Port: 22, User: "deploy"}

// Interrupted transfers wait for their own server. Connecting somewhere else
// must not check, resume or restart them there — their paths name files on
// the server they were queued on.
func TestInterruptedRecoveryIgnoresRowsFromAnotherServer(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model.transfers = []domain.Transfer{{
		ID: 1, Direction: domain.Upload, Source: "/local/site.tar", Destination: "/srv/site.tar",
		BytesTotal: 100, Status: domain.Failed, RetryOnReconnect: true, Server: credentialKey(otherServer),
	}}

	if cmd := model.startInterruptedRecovery(); cmd != nil {
		t.Fatal("recovery started for a transfer that belongs to another server")
	}
	if !model.transfers[0].RetryOnReconnect {
		t.Fatal("the row lost its recovery mark, so its own server could no longer recover it")
	}
	model.applyInterruptedRecovery(recoveryScanMsg{mismatched: map[int]string{}, unreachable: map[int]string{}})
	if !model.transfers[0].RetryOnReconnect {
		t.Fatal("applying another server's recovery cleared this row's mark")
	}
}

func TestInterruptedRecoveryStillRunsForRowsFromThisServer(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model.transfers = []domain.Transfer{{
		ID: 1, Direction: domain.Upload, Source: "/local/site.tar", Destination: "/srv/site.tar",
		BytesTotal: 100, Status: domain.Failed, RetryOnReconnect: true, Server: model.transferServer(),
	}}
	if cmd := model.startInterruptedRecovery(); cmd == nil {
		t.Fatal("recovery did not start for this server's own interrupted transfer")
	}
}

func TestRetryRefusesATransferFromAnotherServer(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model.transfers = []domain.Transfer{{
		ID: 1, Direction: domain.Upload, Source: "/local/site.tar", Destination: "/srv/site.tar",
		BytesTotal: 100, Status: domain.Failed, Server: credentialKey(otherServer),
	}}

	model = press(t, model, runes("R"))

	if len(model.transfers) != 1 {
		t.Fatalf("transfers = %+v, want no retry queued against the wrong server", model.transfers)
	}
	if !strings.Contains(model.status, "another server") {
		t.Fatalf("status = %q, want it to say the transfer belongs to another server", model.status)
	}
}

func TestQueuedTransfersRecordTheirServer(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model.commitScan(preflightScan{direction: domain.Upload, files: []preflightFile{{src: "/l/a", dst: "/r/a", name: "a", size: 1}}})
	if got := model.transfers[len(model.transfers)-1].Server; got != model.transferServer() || got == "" {
		t.Fatalf("queued transfer Server = %q, want %q", got, model.transferServer())
	}
}
