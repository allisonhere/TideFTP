package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"tideftp/internal/domain"
	"tideftp/internal/netcheck"
	"tideftp/internal/transfer"
)

// connectivityModel is a connected model with the check switched on, which is
// the state every probe test starts from.
func connectivityModel(t *testing.T) Model {
	t.Helper()
	model := loadedModel(t, newScriptedEngine())
	model.checkConnectivity = true
	return model
}

func TestConnectivityProbeWaitsForConsecutiveFailures(t *testing.T) {
	model := connectivityModel(t)

	if cmd := model.noteTransferOutcome(transfer.Event{Kind: transfer.Failed}); cmd != nil {
		t.Fatal("probed on the first failure")
	}
	if model.connectivity.phase != connectivityIdle {
		t.Fatalf("phase after one failure = %v, want idle", model.connectivity.phase)
	}

	if cmd := model.noteTransferOutcome(transfer.Event{Kind: transfer.Failed}); cmd == nil {
		t.Fatal("did not probe after the second consecutive failure")
	}
	if model.connectivity.phase != connectivityChecking {
		t.Fatalf("phase after the threshold = %v, want checking", model.connectivity.phase)
	}
}

func TestConnectivitySuccessResetsTheFailureStreak(t *testing.T) {
	model := connectivityModel(t)

	model.noteTransferOutcome(transfer.Event{Kind: transfer.Failed})
	model.noteTransferOutcome(transfer.Event{Kind: transfer.Completed})
	if model.transferFailureStreak != 0 {
		t.Fatalf("streak after a success = %d, want 0", model.transferFailureStreak)
	}

	if cmd := model.noteTransferOutcome(transfer.Event{Kind: transfer.Failed}); cmd != nil {
		t.Fatal("one failure after a success should not earn a probe")
	}
}

func TestConnectivityCheckOffDisablesProbing(t *testing.T) {
	model := connectivityModel(t)
	model.checkConnectivity = false

	for range connectivityFailureThreshold + 1 {
		if cmd := model.noteTransferOutcome(transfer.Event{Kind: transfer.Failed}); cmd != nil {
			t.Fatal("probed while the connectivity check was switched off")
		}
	}
}

// TestConnectivityHoldStopsTheQueue pins the whole point of the feature: while
// the check is running or has paused, no new transfer is promoted.
func TestConnectivityHoldStopsTheQueue(t *testing.T) {
	model := connectivityModel(t)
	model.transfers = []domain.Transfer{{ID: 1, Status: domain.Queued}}

	for _, phase := range []connectivityPhase{connectivityChecking, connectivityPaused} {
		model.transfers[0].Status = domain.Queued
		model.connectivity = connectivityState{phase: phase}
		model.startQueuedTransfers()
		if model.transfers[0].Status != domain.Queued {
			t.Fatalf("queue phase %v started a transfer", phase)
		}
	}
}

func TestUnreachableProbePausesTheQueue(t *testing.T) {
	model := connectivityModel(t)
	model.transfers = []domain.Transfer{{ID: 1, Status: domain.Queued}}
	model.connectivityToken = 7
	model.connectivity = connectivityState{phase: connectivityChecking, token: 7}

	cmd := model.applyConnectivityResult(connectivityResultMsg{
		token:  7,
		result: netcheck.Result{Status: netcheck.NoLink, Detail: "no network connection"},
	})

	if model.connectivity.phase != connectivityPaused {
		t.Fatalf("phase = %v, want paused", model.connectivity.phase)
	}
	if cmd == nil {
		t.Fatal("a paused queue did not schedule a re-probe")
	}
	if model.connectivityBannerRows() != 1 {
		t.Fatal("a paused queue did not show its banner")
	}
	if !strings.Contains(model.status, "no network connection") {
		t.Fatalf("status = %q, want it to name the local network", model.status)
	}
}

func TestReachableProbeResumesTheQueue(t *testing.T) {
	model := connectivityModel(t)
	model.transfers = []domain.Transfer{{ID: 1, Status: domain.Queued}}
	model.connectivityToken = 7
	model.connectivity = connectivityState{phase: connectivityPaused, token: 7, attempt: 3}

	model.applyConnectivityResult(connectivityResultMsg{
		token:  7,
		result: netcheck.Result{Status: netcheck.Reachable},
	})

	if model.connectivity.phase != connectivityIdle {
		t.Fatalf("phase = %v, want idle after the network returned", model.connectivity.phase)
	}
	if model.transfers[0].Status != domain.Active {
		t.Fatalf("queued transfer status = %v, want it resumed", model.transfers[0].Status)
	}
}

func TestStaleConnectivityResultIsIgnored(t *testing.T) {
	model := connectivityModel(t)
	model.connectivityToken = 7
	model.connectivity = connectivityState{phase: connectivityChecking, token: 7}

	model.applyConnectivityResult(connectivityResultMsg{
		token:  99,
		result: netcheck.Result{Status: netcheck.NoLink},
	})

	if model.connectivity.phase != connectivityChecking {
		t.Fatalf("a stale result changed the phase to %v", model.connectivity.phase)
	}
}

func TestDisconnectClearsConnectivityPause(t *testing.T) {
	model := connectivityModel(t)
	model.connectivityToken = 7
	model.connectivity = connectivityState{phase: connectivityPaused, token: 7}
	model.transferFailureStreak = 3

	model = dropped(model, "connection reset by peer")

	if model.connectivity.phase != connectivityIdle {
		t.Fatalf("phase after a drop = %v, want the reconnect flow to own it", model.connectivity.phase)
	}
	if model.transferFailureStreak != 0 {
		t.Fatalf("streak after a drop = %d, want 0", model.transferFailureStreak)
	}
}

func TestConnectivityBannerNamesTheProblem(t *testing.T) {
	model := connectivityModel(t)
	model.connectivityToken = 1
	model.connectivity = connectivityState{phase: connectivityPaused, status: netcheck.NoLink, token: 1}

	if view := ansi.Strip(model.View()); !strings.Contains(view, "no network connection") {
		t.Fatalf("banner does not name the local network:\n%s", view)
	}

	model.connectivity.status = netcheck.HostUnreachable
	if view := ansi.Strip(model.View()); !strings.Contains(view, "server unreachable") {
		t.Fatalf("banner does not distinguish the server being down:\n%s", view)
	}
}

// TestConnectivityRetryUsesBackoff guards the schedule from collapsing to a
// hot loop: later attempts wait at least as long as earlier ones.
func TestConnectivityRetryUsesBackoff(t *testing.T) {
	if got := connectivityRetryDelay(0); got != connectivityRetryDelays[0] {
		t.Fatalf("first retry delay = %v, want %v", got, connectivityRetryDelays[0])
	}
	last := connectivityRetryDelays[len(connectivityRetryDelays)-1]
	if got := connectivityRetryDelay(len(connectivityRetryDelays)); got != last {
		t.Fatalf("past-the-end retry delay = %v, want the last entry %v", got, last)
	}
	for i := 1; i < len(connectivityRetryDelays); i++ {
		if connectivityRetryDelays[i] < connectivityRetryDelays[i-1] {
			t.Fatalf("retry schedule goes backwards at %d", i)
		}
	}
}

// TestConnectivityRetryTickReprobes covers the tick path end to end: a paused
// queue spends another probe on its backoff.
func TestConnectivityRetryTickReprobes(t *testing.T) {
	model := connectivityModel(t)
	model.connectivityToken = 7
	model.connectivity = connectivityState{phase: connectivityPaused, token: 7}

	cmd := model.applyConnectivityRetry(connectivityRetryMsg{token: 7})

	if cmd == nil {
		t.Fatal("a retry tick did not start a new probe")
	}
	if model.connectivity.phase != connectivityChecking {
		t.Fatalf("phase = %v, want checking after the retry tick", model.connectivity.phase)
	}
	if cmd := model.applyConnectivityRetry(connectivityRetryMsg{token: 99}); cmd != nil {
		t.Fatal("a stale retry tick started a probe")
	}
}

// TestConnectivityBackoffAdvancesAcrossRetries catches the easy mistake of
// rebuilding the paused state with a fresh attempt counter, which would pin
// the queue to the first (shortest) retry delay forever.
func TestConnectivityBackoffAdvancesAcrossRetries(t *testing.T) {
	model := connectivityModel(t)
	model.connectivityToken = 7
	model.connectivity = connectivityState{phase: connectivityChecking, token: 7}
	unreachable := connectivityResultMsg{token: 7, result: netcheck.Result{Status: netcheck.HostUnreachable}}

	model.applyConnectivityResult(unreachable)
	if model.connectivity.attempt != 1 {
		t.Fatalf("attempt after the first pause = %d, want 1", model.connectivity.attempt)
	}

	model.applyConnectivityRetry(connectivityRetryMsg{token: 7})
	model.applyConnectivityResult(unreachable)
	if model.connectivity.attempt != 2 {
		t.Fatalf("attempt after the second pause = %d, want 2 (backoff must advance)", model.connectivity.attempt)
	}
}

// TestConnectivityRetryCmdIsANoOpWhenIdle guards against a parked tick from an
// outage that already resolved.
func TestConnectivityRetryCmdIsANoOpWhenIdle(t *testing.T) {
	model := connectivityModel(t)
	model.connectivityToken = 7
	model.connectivity = connectivityState{token: 7}

	if cmd := model.applyConnectivityRetry(connectivityRetryMsg{token: 7}); cmd != nil {
		t.Fatal("an idle model reacted to a retry tick")
	}
}
