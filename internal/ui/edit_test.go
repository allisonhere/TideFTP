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

// deliverEditorClosed simulates the editor exiting cleanly and runs whatever
// the model does next (read the temp file back, write it if changed).
func deliverEditorClosed(t *testing.T, model Model) Model {
	t.Helper()
	updated, cmd := model.Update(editorClosedMsg{})
	return settle(t, updated.(Model), cmd)
}

func TestEditChecksOutTheHighlightedFile(t *testing.T) {
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
