package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
)

// droppedModel is a model whose connection has gone away, leaving remoteFS
// and engine nil. Everything reaching for a filesystem through fsByID gets a
// nil interface from here, and calling through one panics.
func droppedModel(t *testing.T) Model {
	t.Helper()
	model, _ := loadedModelWithDialer(t, &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()})
	conn := model.conn
	model = settle(t, model, model.disconnect())
	model = settle(t, model, func() tea.Msg { return disconnectedMsg{conn: conn} })
	if model.remoteFS != nil {
		t.Fatal("expected the remote filesystem to be gone")
	}
	return model
}

// The remote pane has no filesystem behind it once the connection drops, so
// focus must not land there — every action that then reaches through fsByID
// would panic on a nil interface.
func TestFocusRefusesTheRemotePaneWhileDisconnected(t *testing.T) {
	model := droppedModel(t)

	model = press(t, model, runes("l"))
	if model.focus == focusRemote {
		t.Fatal("focus moved to the remote pane while disconnected")
	}
	if !model.statusErr || !strings.Contains(model.status, "not connected") {
		t.Fatalf("status = %q, want a not-connected error", model.status)
	}
}

// Backspace on a remote pane focused while disconnected used to call Parent
// on a nil vfs.FS and take the whole app down.
func TestParentDirDoesNotPanicWithNoFilesystem(t *testing.T) {
	model := droppedModel(t)
	// Force the state the focus guard now prevents, so the guard inside
	// parentDir is exercised on its own rather than shadowed by it.
	model.focus = focusRemote

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("parentDir panicked with no filesystem: %v", r)
		}
	}()
	if cmd := model.parentDir(); cmd != nil {
		t.Fatal("parentDir must do nothing when there is no filesystem")
	}
}

// A delete/rename/chmod prompt can be left open across a drop. Confirming it
// afterwards used to hand fileActionCmd a nil filesystem and panic.
func TestFileActionAfterADropReportsRatherThanPanics(t *testing.T) {
	model := droppedModel(t)
	model.fileAction = &fileActionPrompt{
		kind:    fileActionDelete,
		pane:    paneRemote,
		entries: []domain.Entry{{Name: "doomed.txt"}},
	}
	model.overlay = overlayFileAction

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("confirming a file action after a drop panicked: %v", r)
		}
	}()
	cmd := model.submitFileAction()

	if cmd != nil {
		t.Fatal("no command should be issued without a filesystem")
	}
	if !model.statusErr || !strings.Contains(model.status, "not connected") {
		t.Fatalf("status = %q, want a not-connected error", model.status)
	}
	if model.overlay != overlayNone || model.fileAction != nil {
		t.Fatal("the prompt should be dismissed rather than left open")
	}
}

// The editor is the one place the user spends minutes away from the app, so a
// drop while it is open is entirely ordinary. Writing back through a nil
// filesystem used to panic — and the deferred cleanup ran on the way down,
// deleting the temp file and the user's work with it.
func TestEditSaveAfterADropKeepsTheWorkAndDoesNotPanic(t *testing.T) {
	model := droppedModel(t)

	tmp := filepath.Join(t.TempDir(), "edited.conf")
	if err := os.WriteFile(tmp, []byte("edited by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	model.pendingEdit = &pendingEdit{
		pane:    paneRemote,
		path:    "/etc/nginx.conf",
		name:    "nginx.conf",
		tmpPath: tmp,
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("editor close after a drop panicked: %v", r)
		}
	}()
	next, cmd := model.Update(editorClosedMsg{})
	model = next.(Model)
	if cmd != nil {
		t.Fatal("no save should be attempted without a filesystem")
	}

	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("the edited temp file must survive a drop, not be deleted with the panic: %v", err)
	}
	if !model.statusErr || !strings.Contains(model.status, tmp) {
		t.Fatalf("status = %q, want it to name %s so the work can be recovered", model.status, tmp)
	}
}
