package ui

import (
	"errors"
	"strings"
	"testing"

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
	if !strings.Contains(model.status, "connection lost") || !strings.Contains(model.status, "attempt 1/5") {
		t.Fatalf("status = %q, want the drop and the next attempt", model.status)
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
