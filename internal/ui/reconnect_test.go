package ui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
	"tideftp/internal/session"
)

// dropped feeds an unexpected disconnect in and returns the model plus
// whatever it wants to do next — a reconnect campaign returns a timer, which
// tests fire by hand rather than waiting on.
func dropped(model Model, reason string) Model {
	next, _ := model.Update(disconnectedMsg{conn: model.conn, err: errors.New(reason)})
	return next.(Model)
}

func TestDroppedConnectionStartsAReconnectCampaign(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model = settle(t, model, model.navigateTo(paneRemote, "/releases"))

	model = dropped(model, "connection reset by peer")

	if model.reconnect == nil {
		t.Fatalf("an unexpected drop did not start a reconnect campaign; status=%q", model.status)
	}
	if model.reconnect.resumePath != "/releases" {
		t.Fatalf("resumePath = %q, want the directory the user was in", model.reconnect.resumePath)
	}
	if !strings.Contains(model.status, "connection lost") || !strings.Contains(model.status, "attempt 1/8") {
		t.Fatalf("status = %q, want the drop and the next attempt", model.status)
	}
	if !strings.Contains(model.connectionSummary(), "reconnecting") || !strings.Contains(model.connectionSummary(), "attempt 1/8") {
		t.Fatalf("connection summary = %q, want visible reconnect state", model.connectionSummary())
	}
}

func TestInterruptedRecoveryResumesOnlyCompatiblePartialDestinations(t *testing.T) {
	remote := fakefs.NewRemote()
	local := fakefs.NewRemote()
	payload := bytes.Repeat([]byte("TideFTP reconnect test\n"), 5000)
	if err := local.WriteFile(context.Background(), "/incoming/partial.bin", payload); err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteFile(context.Background(), "/incoming/.partial-upload", payload[:32768]); err != nil {
		t.Fatal(err)
	}
	candidates := []domain.Transfer{
		{ID: 1, Direction: domain.Upload, Source: "/local/new.bin", Destination: "/incoming/new.bin", BytesTotal: 100},
		{ID: 2, Direction: domain.Upload, Source: "/incoming/partial.bin", Destination: "/incoming/.partial-upload", BytesTotal: int64(len(payload))},
		{ID: 3, Direction: domain.Upload, Source: "/local/full.bin", Destination: "/incoming/client-drop.zip", BytesTotal: 10},
	}
	result := runInterruptedRecovery(context.Background(), 7, candidates, local, remote, nil)
	if len(result.partial) != 1 || len(result.missing) != 1 {
		t.Fatalf("recoveries = %+v, want one verified partial and one missing destination", result)
	}
	if result.partial[0].offset != 32768 {
		t.Fatalf("partial offset = %d, want the existing partial size", result.partial[0].offset)
	}
	if result.missing[0].offset != 0 {
		t.Fatalf("missing offset = %d, want zero", result.missing[0].offset)
	}
	if !strings.Contains(result.mismatched[3], "already full") {
		t.Fatalf("full destination recovery result = %q, want a decision", result.mismatched[3])
	}
}

func TestInterruptedRecoveryQueuesCheckedTransfersAfterReconnect(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model.transfers = []domain.Transfer{{ID: 1, Direction: domain.Upload, Source: "/local/partial.bin", Destination: "/incoming/.partial-upload", BytesTotal: 100000, Status: domain.Failed, RetryOnReconnect: true}}

	model.applyInterruptedRecovery(recoveryScanMsg{partial: []recoveryRetry{{originalID: 1, direction: domain.Upload, source: "/local/partial.bin", destination: "/incoming/.partial-upload", size: 100000, offset: 32768, protocol: "sftp"}}})
	if model.transfers[0].RetryOnReconnect {
		t.Fatal("original interrupted transfer remained armed after recovery")
	}
	if model.overlay != overlayRecovery || model.recoverySummary == nil {
		t.Fatal("recovery did not open its review panel")
	}
	if len(model.transfers) != 1 {
		t.Fatal("recovery queued transfers before the user resumed them")
	}

	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.overlay != overlayNone || model.recoverySummary != nil {
		t.Fatal("enter did not resolve and close the review panel")
	}
	if len(model.transfers) != 2 {
		t.Fatalf("transfers = %+v, want original and recovery row", model.transfers)
	}
	row := model.transfers[1]
	if row.ResumeFrom != 32768 || row.BytesDone != 32768 || row.Status != domain.Active {
		t.Fatalf("recovery row = %+v, want active transfer resuming from checked offset", row)
	}
}

func TestDeliberateDisconnectDoesNotReconnect(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})

	next, _ := model.Update(disconnectedMsg{conn: model.conn, err: nil})
	model = next.(Model)

	if model.reconnect != nil {
		t.Fatalf("a disconnect the user asked for must not be undone by a redial")
	}
}

func TestAutoReconnectOffLeavesTheDropAlone(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model.autoReconnect = false

	model = dropped(model, "connection reset by peer")

	if model.reconnect != nil {
		t.Fatalf("reconnecting is off, but a campaign started anyway")
	}
	if !strings.Contains(model.status, "connection lost") {
		t.Fatalf("status = %q, want the drop reported plainly", model.status)
	}
}

func TestReconnectRedialsAndRestoresTheDirectory(t *testing.T) {
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model, _ := loadedModelWithDialer(t, dialer)
	model = settle(t, model, model.navigateTo(paneRemote, "/releases"))
	model = dropped(model, "connection reset by peer")
	dialsBefore := len(dialer.calls)

	// Fire the backoff timer by hand, then settle the dial it starts.
	next, cmd := model.Update(reconnectTickMsg{token: model.reconnect.token})
	model = settle(t, next.(Model), cmd)

	if len(dialer.calls) != dialsBefore+1 {
		t.Fatalf("dials = %d, want one redial", len(dialer.calls)-dialsBefore)
	}
	if !model.connected() {
		t.Fatalf("state = %v after a successful redial, want connected", model.state)
	}
	if model.remote.path != "/releases" {
		t.Fatalf("remote path = %q after reconnecting, want the directory the drop interrupted", model.remote.path)
	}
	if model.reconnect != nil {
		t.Fatalf("a campaign that connected must retire itself")
	}
}

func TestReconnectBacksOffThenGivesUp(t *testing.T) {
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model, _ := loadedModelWithDialer(t, dialer)
	model = dropped(model, "connection reset by peer")
	dialer.err = errors.New("no route to host")

	for attempt := 1; attempt <= len(reconnectDelays); attempt++ {
		if model.reconnect == nil {
			t.Fatalf("campaign ended after %d attempts, want %d", attempt-1, len(reconnectDelays))
		}
		next, cmd := model.Update(reconnectTickMsg{token: model.reconnect.token})
		model = settle(t, next.(Model), cmd)
	}

	if model.reconnect != nil {
		t.Fatalf("campaign still running past its schedule of %d attempts", len(reconnectDelays))
	}
	if !strings.Contains(model.status, "gave up") {
		t.Fatalf("status = %q, want it to say the app stopped trying", model.status)
	}
}

func TestStaleReconnectTickIsIgnored(t *testing.T) {
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model, _ := loadedModelWithDialer(t, dialer)
	model = dropped(model, "connection reset by peer")
	stale := model.reconnect.token

	// The user connects somewhere by hand, which calls the campaign off.
	other := session.Target{Name: "other", Protocol: "sftp", Host: "other.local", User: "allie"}
	model = settle(t, model, model.connect(other, session.Credentials{}))
	dialsBefore := len(dialer.calls)

	next, cmd := model.Update(reconnectTickMsg{token: stale})
	model = settle(t, next.(Model), cmd)

	if len(dialer.calls) != dialsBefore {
		t.Fatalf("a timer from a campaign the user cancelled still redialled")
	}
}

func TestReconnectRedialsWithTheLastCredentials(t *testing.T) {
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model, _ := loadedModelWithDialer(t, dialer)
	model = settle(t, model, model.connect(testTarget, session.Credentials{Password: "hunter2"}))
	model = dropped(model, "connection reset by peer")

	next, cmd := model.Update(reconnectTickMsg{token: model.reconnect.token})
	model = settle(t, next.(Model), cmd)

	last := dialer.creds[len(dialer.creds)-1]
	if last.Password != "hunter2" {
		t.Fatalf("redial creds = %+v, want the credentials the connection was opened with", last)
	}
}

func TestDropStillFailsInFlightTransfersWhileReconnecting(t *testing.T) {
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model.transfers = []domain.Transfer{{ID: 1, BytesTotal: 100, Status: domain.Active}}

	model = dropped(model, "connection reset by peer")

	if model.transfers[0].Status != domain.Failed {
		t.Fatalf("status = %v, want a transfer stopped by the drop to fail whether or not a redial follows", model.transfers[0].Status)
	}
}

// recoveryPanelModel is a model with the review panel open over four
// interrupted transfers, one per category, so each bulk action's effect on
// every category is observable.
func recoveryPanelModel(t *testing.T) (Model, recoverySummary) {
	t.Helper()
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	model.width, model.height = 100, 30
	model.transfers = []domain.Transfer{
		{ID: 1, Direction: domain.Upload, Source: "/local/a.bin", Destination: "/incoming/a.bin", BytesTotal: 1000, Status: domain.Failed},
		{ID: 2, Direction: domain.Upload, Source: "/local/b.bin", Destination: "/incoming/b.bin", BytesTotal: 1000, Status: domain.Failed},
		{ID: 3, Direction: domain.Upload, Source: "/local/c.bin", Destination: "/incoming/c.bin", BytesTotal: 1000, Status: domain.Failed},
		{ID: 4, Direction: domain.Upload, Source: "/local/d.bin", Destination: "/incoming/d.bin", BytesTotal: 1000, Status: domain.Failed},
	}
	summary := recoverySummary{
		partial:     []recoveryRetry{{originalID: 1, direction: domain.Upload, source: "/local/a.bin", destination: "/incoming/a.bin", size: 1000, offset: 400, protocol: "sftp"}},
		missing:     []recoveryRetry{{originalID: 2, direction: domain.Upload, source: "/local/b.bin", destination: "/incoming/b.bin", size: 1000, protocol: "sftp"}},
		mismatched:  map[int]string{3: "partial could not be verified; retry with R"},
		unreachable: map[int]string{4: "could not inspect destination; retry with R"},
	}
	model.recoverySummary = &summary
	model.overlay = overlayRecovery
	return model, summary
}

// TestRecoveryPanelListsCategoriesAndCommands pins the panel the user
// actually reads: every category and every key the footer advertises.
func TestRecoveryPanelListsCategoriesAndCommands(t *testing.T) {
	model, _ := recoveryPanelModel(t)

	view := ansi.Strip(model.View())
	for _, want := range []string{
		"1 verified partial(s)", "safe to resume",
		"1 destination(s) missing", "safe to restart",
		"1 mismatched/full", "need a decision",
		"1 unreachable", "leave failed",
		"resume safe (2)", "restart remaining", "skip remaining",
		"inspect individual exceptions", "decide later",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("recovery panel missing %q:\n%s", want, view)
		}
	}
}

func TestRecoveryPanelEnterResumesSafeOnly(t *testing.T) {
	model, _ := recoveryPanelModel(t)

	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if model.overlay != overlayNone || model.recoverySummary != nil {
		t.Fatal("enter did not close the panel")
	}
	if len(model.transfers) != 6 {
		t.Fatalf("transfers = %d, want the four originals plus the two safe resumes", len(model.transfers))
	}
	if model.transfers[4].ResumeFrom != 400 {
		t.Fatalf("verified partial did not resume from its offset: %+v", model.transfers[4])
	}
	// The decisions and the unreachable row are untouched and still Failed.
	if model.transfers[2].Status != domain.Failed || model.transfers[3].Status != domain.Failed {
		t.Fatal("resume safe should not touch the mismatched or unreachable rows")
	}
}

func TestRecoveryPanelRestartRemainingResumesSafeAndRestartsMismatched(t *testing.T) {
	model, _ := recoveryPanelModel(t)

	model = press(t, model, runes("r"))

	if len(model.transfers) != 7 {
		t.Fatalf("transfers = %d, want two safe resumes plus one restarted mismatched", len(model.transfers))
	}
	restarted := model.transfers[len(model.transfers)-1]
	if restarted.BytesDone != 0 || restarted.ResumeFrom != 0 {
		t.Fatalf("restarted row should start from zero: %+v", restarted)
	}
	// The unreachable row is never bulk-restarted.
	if model.transfers[3].Status != domain.Failed {
		t.Fatal("restart remaining must leave the unreachable row failed")
	}
}

func TestRecoveryPanelSkipResumesSafeAndLeavesMismatched(t *testing.T) {
	model, _ := recoveryPanelModel(t)

	model = press(t, model, runes("s"))

	if len(model.transfers) != 6 {
		t.Fatalf("transfers = %d, want only the two safe resumes", len(model.transfers))
	}
	if model.transfers[3].Status != domain.Failed {
		t.Fatal("skip remaining must leave the mismatched row failed")
	}
}

func TestRecoveryPanelInspectJumpsToFailedRows(t *testing.T) {
	model, _ := recoveryPanelModel(t)

	model = press(t, model, tea.KeyMsg{Type: tea.KeyDown})

	if model.overlay != overlayNone || model.recoverySummary != nil {
		t.Fatal("inspect did not close the panel")
	}
	if model.focus != focusQueue || model.bottomTab != tabFailed {
		t.Fatalf("inspect left focus=%v tab=%v, want the Failed rows", model.focus, model.bottomTab)
	}
}

func TestRecoveryPanelEscDecidesLater(t *testing.T) {
	model, _ := recoveryPanelModel(t)

	model = press(t, model, tea.KeyMsg{Type: tea.KeyEsc})

	if model.overlay != overlayNone || model.recoverySummary != nil {
		t.Fatal("esc did not close the panel")
	}
	if len(model.transfers) != 4 {
		t.Fatalf("esc queued %d transfer(s), want none", len(model.transfers)-4)
	}
}
