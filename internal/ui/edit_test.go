package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tideftp/internal/config"
	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
	"tideftp/internal/localfs"
)

// stubEditor points $EDITOR at a program that is always on PATH and exits 0,
// so editor resolution is deterministic regardless of what the test box has
// installed. The editor never actually runs under the test harness anyway —
// tea.ExecProcess's execMsg is inert — but startEdit checks PATH up front.
func stubEditor(t *testing.T) {
	t.Helper()
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "true")
}

// deliverEditorClosed simulates the editor exiting cleanly and runs whatever
// the model does next (read the temp file back, write it if changed).
func deliverEditorClosed(t *testing.T, model Model) Model {
	t.Helper()
	updated, cmd := model.Update(editorClosedMsg{})
	return settle(t, updated.(Model), cmd)
}

func TestEditChecksOutTheHighlightedFile(t *testing.T) {
	stubEditor(t)
	remote := fakefs.NewRemote()
	model := loadedModelOver(t, localfs.New(), remote, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/public_html"))
	for i, e := range model.remote.entries {
		if e.Name == "robots.txt" {
			model.remote.cursor = i
		}
	}

	model = press(t, model, runes("e"))

	if model.pendingEdit == nil {
		t.Fatalf("pressing e did not check out a file; status=%q", model.status)
	}
	if model.pendingEdit.name != "robots.txt" {
		t.Fatalf("pendingEdit.name = %q, want robots.txt", model.pendingEdit.name)
	}
	body, err := os.ReadFile(model.pendingEdit.tmpPath)
	if err != nil {
		t.Fatalf("temp file not written: %v", err)
	}
	want, _ := remote.ReadFile(t.Context(), "/public_html/robots.txt")
	if string(body) != string(want) {
		t.Fatalf("temp file = %q, want the source contents %q", body, want)
	}
	os.Remove(model.pendingEdit.tmpPath)
}

func TestEditWritesChangedContentsBack(t *testing.T) {
	stubEditor(t)
	remote := fakefs.NewRemote()
	model := loadedModelOver(t, localfs.New(), remote, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/public_html"))
	for i, e := range model.remote.entries {
		if e.Name == "robots.txt" {
			model.remote.cursor = i
		}
	}
	model = press(t, model, runes("e"))
	tmp := model.pendingEdit.tmpPath

	if err := os.WriteFile(tmp, []byte("User-agent: *\nDisallow: /secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	model = deliverEditorClosed(t, model)

	got, err := remote.ReadFile(t.Context(), "/public_html/robots.txt")
	if err != nil || string(got) != "User-agent: *\nDisallow: /secret\n" {
		t.Fatalf("remote file after edit = %q, %v; want the edited contents", got, err)
	}
	if model.pendingEdit != nil {
		t.Fatalf("pendingEdit should be cleared after the editor closes")
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("temp file was not cleaned up")
	}
}

func TestEditSkipsWriteWhenUnchanged(t *testing.T) {
	stubEditor(t)
	remote := fakefs.NewRemote()
	before, _ := remote.ReadFile(t.Context(), "/public_html/robots.txt")

	model := loadedModelOver(t, localfs.New(), remote, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/public_html"))
	for i, e := range model.remote.entries {
		if e.Name == "robots.txt" {
			model.remote.cursor = i
		}
	}
	model = press(t, model, runes("e"))

	// No edit to the temp file.
	model = deliverEditorClosed(t, model)

	after, _ := remote.ReadFile(t.Context(), "/public_html/robots.txt")
	if string(after) != string(before) {
		t.Fatalf("remote file changed on a no-op edit: %q -> %q", before, after)
	}
	if model.statusErr || !strings.Contains(model.status, "unchanged") {
		t.Fatalf("status = %q (err=%v), want an 'unchanged' note", model.status, model.statusErr)
	}
}

func TestEditRejectsADirectory(t *testing.T) {
	stubEditor(t)
	remote := fakefs.NewRemote()
	model := loadedModelOver(t, localfs.New(), remote, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/public_html"))
	for i, e := range model.remote.entries {
		if e.Name == "assets" { // a directory
			model.remote.cursor = i
		}
	}

	model = press(t, model, runes("e"))

	if model.pendingEdit != nil {
		t.Fatalf("e on a directory checked out an edit")
	}
	if !model.statusErr {
		t.Fatalf("e on a directory did not raise an error")
	}
}

func TestEditRejectsRemoteWhenNotConnected(t *testing.T) {
	stubEditor(t)
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, nil, config.Default(), nil, nil)
	model.width, model.height = 120, 36
	model.focus = focusRemote
	model.remote.entries = []domain.Entry{{Name: "x.conf", Kind: domain.EntryFile, Size: 10}}
	model.remote.cursor = 0

	model = press(t, model, runes("e"))

	if model.pendingEdit != nil || !model.statusErr {
		t.Fatalf("e on the remote pane while disconnected should error, got pendingEdit=%v status=%q", model.pendingEdit, model.status)
	}
}

func TestEditRejectsAnOversizeFile(t *testing.T) {
	stubEditor(t)
	model := loadedModelOver(t, localfs.New(), fakefs.NewRemote(), newScriptedEngine())
	model.focus = focusLocal
	model.local.entries = []domain.Entry{{Name: "huge.log", Kind: domain.EntryFile, Size: editMaxBytes + 1}}
	model.local.cursor = 0

	model = press(t, model, runes("e"))

	if model.pendingEdit != nil || !model.statusErr {
		t.Fatalf("e on an oversize file should error, got pendingEdit=%v status=%q", model.pendingEdit, model.status)
	}
}

func TestEditRejectsABinaryFile(t *testing.T) {
	stubEditor(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "data.bin")
	if err := os.WriteFile(bin, []byte{'a', 'b', 0x00, 'c'}, 0o644); err != nil {
		t.Fatal(err)
	}

	model := loadedModelOver(t, localfs.New(), fakefs.NewRemote(), newScriptedEngine())
	model.focus = focusLocal
	model.local.path = dir
	model.local.entries = []domain.Entry{{Name: "data.bin", Kind: domain.EntryFile, Size: 4}}
	model.local.cursor = 0

	model = press(t, model, runes("e"))

	if model.pendingEdit != nil {
		os.Remove(model.pendingEdit.tmpPath)
		t.Fatalf("a binary file was checked out for editing")
	}
	if !model.statusErr {
		t.Fatalf("editing a binary file did not raise an error; status=%q", model.status)
	}
}

func TestResolveEditorHonoursTheConfiguredCommand(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")

	argv, err := resolveEditor("true")
	if err != nil || len(argv) != 1 || filepath.Base(argv[0]) != "true" {
		t.Fatalf("resolveEditor(\"true\") = %v, %v; want the resolved path to true", argv, err)
	}

	if _, err := resolveEditor("tideftp-bogus-editor-xyz"); err == nil || !strings.Contains(err.Error(), "tideftp-bogus-editor-xyz") {
		t.Fatalf("resolveEditor of a missing command = %v, want an error naming it", err)
	}
}

func TestEditReportsWhenNoEditorIsAvailable(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "tideftp-no-such-editor-xyz")

	model := loadedModelOver(t, localfs.New(), fakefs.NewRemote(), newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/public_html"))
	for i, e := range model.remote.entries {
		if e.Name == "robots.txt" {
			model.remote.cursor = i
		}
	}

	model = press(t, model, runes("e"))

	if model.pendingEdit != nil {
		os.Remove(model.pendingEdit.tmpPath)
		t.Fatalf("a file was checked out even though no editor is available")
	}
	if !model.statusErr || !strings.Contains(model.status, "PATH") {
		t.Fatalf("status = %q (err=%v), want a not-on-PATH editor error", model.status, model.statusErr)
	}
}
