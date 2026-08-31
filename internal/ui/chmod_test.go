package ui

import (
	"context"
	"io"
	"io/fs"
	"path"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
	"tideftp/internal/vfs"
)

// unsupportedChmodFS is a vfs.FS whose Chmod always refuses with
// vfs.ErrUnsupported, like the FTP adapter. Everything else is a benign stub.
type unsupportedChmodFS struct{}

func (unsupportedChmodFS) List(context.Context, string, bool) ([]domain.Entry, error) {
	return []domain.Entry{{Name: "notes.txt", Kind: domain.EntryFile, Mode: "-rw-r--r--"}}, nil
}
func (unsupportedChmodFS) Child(current, name string) string            { return path.Join(current, name) }
func (unsupportedChmodFS) Parent(current string) string                 { return path.Dir(current) }
func (unsupportedChmodFS) Mkdir(context.Context, string) error          { return nil }
func (unsupportedChmodFS) Rename(context.Context, string, string) error { return nil }
func (unsupportedChmodFS) Remove(context.Context, string) error         { return nil }
func (unsupportedChmodFS) Chmod(context.Context, string, fs.FileMode) error {
	return vfs.ErrUnsupported
}
func (unsupportedChmodFS) ReadFile(context.Context, string) ([]byte, error) { return nil, nil }
func (unsupportedChmodFS) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (unsupportedChmodFS) WriteFile(context.Context, string, []byte) error { return nil }

func TestParseChmodMode(t *testing.T) {
	cases := []struct {
		in   string
		want fs.FileMode
		ok   bool
	}{
		{"644", 0o644, true},
		{"0755", 0o755, true},
		{" 600 ", 0o600, true},
		{"2775", 0o775 | fs.ModeSetgid, true},
		{"4755", 0o755 | fs.ModeSetuid, true},
		{"1777", 0o777 | fs.ModeSticky, true},
		{"", 0, false},
		{"88", 0, false},    // not octal
		{"rwx", 0, false},   // not digits
		{"77777", 0, false}, // out of range
	}
	for _, c := range cases {
		got, err := parseChmodMode(c.in)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("parseChmodMode(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("parseChmodMode(%q) = %v, nil; want an error", c.in, got)
		}
	}
}

func TestModeStringToOctal(t *testing.T) {
	cases := map[string]string{
		"-rw-r--r--": "644",
		"drwxr-xr-x": "755",
		"-rwxrwxrwx": "777",
		"----------": "000",
		"-rw-------": "600",
		"dir":        "", // FTP-style kind label, not a permission string
		"file":       "",
	}
	for in, want := range cases {
		if got := modeStringToOctal(in); got != want {
			t.Errorf("modeStringToOctal(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMChmodPromptPrefillsCurrentMode(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/public_html"))
	// cursor 0 is "assets" (a dir, drwxr-xr-x) after the dirs-first sort
	model = press(t, model, runes("m"))

	if model.overlay != overlayFileAction || model.fileAction == nil {
		t.Fatalf("'m' did not open the chmod prompt (overlay=%v)", model.overlay)
	}
	if model.fileAction.kind != fileActionChmod {
		t.Fatalf("prompt kind = %v, want chmod", model.fileAction.kind)
	}
	if model.fileAction.text != "755" {
		t.Fatalf("prefill = %q, want 755 for a drwxr-xr-x dir", model.fileAction.text)
	}
}

func TestChmodFlowChangesTheModeAndRefreshes(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/public_html"))

	// Point the cursor at a file: index.html, -rw-r--r-- in the fake tree.
	target := "index.html"
	for i, e := range model.remote.entries {
		if e.Name == target {
			model.remote.cursor = i
		}
	}
	if got, _ := model.remote.current(); got.Name != target {
		t.Fatalf("setup: cursor on %q, want %q", got.Name, target)
	}

	model = press(t, model, runes("m"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlU}) // clear prefill
	model = press(t, model, runes("600"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if model.overlay != overlayNone {
		t.Fatalf("overlay still open after submit: %v", model.overlay)
	}
	var mode string
	for _, e := range model.remote.entries {
		if e.Name == target {
			mode = e.Mode
		}
	}
	if mode != "-rw-------" {
		t.Fatalf("mode after chmod 600 = %q, want -rw-------", mode)
	}
}

func TestChmodRejectsABadModeWithoutClosing(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/public_html"))

	model = press(t, model, runes("m"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlU})
	model = press(t, model, runes("999")) // 9 is not an octal digit
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if model.overlay != overlayFileAction {
		t.Fatalf("a bad mode closed the prompt (overlay=%v)", model.overlay)
	}
	if !model.statusErr {
		t.Fatalf("a bad mode did not report an error")
	}
}

func TestChmodOnMultipleSelectedEntries(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/public_html"))
	for _, name := range []string{"index.html", "app.css", "robots.txt"} {
		model.remote.selected[name] = true
	}

	model = press(t, model, runes("m"))
	if model.fileAction == nil || len(model.fileAction.entries) != 3 {
		t.Fatalf("chmod prompt entries = %d, want the 3 selected", len(model.fileAction.entries))
	}
	if model.fileAction.oldName != "3 items" {
		t.Fatalf("label = %q, want \"3 items\"", model.fileAction.oldName)
	}
	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlU})
	model = press(t, model, runes("640"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	changed := 0
	for _, e := range model.remote.entries {
		switch e.Name {
		case "index.html", "app.css", "robots.txt":
			if e.Mode == "-rw-r-----" {
				changed++
			}
		}
	}
	if changed != 3 {
		t.Fatalf("chmod applied to %d of 3 selected entries", changed)
	}
}

func TestChmodUnsupportedIsReportedAsAnError(t *testing.T) {
	// vfs.ErrUnsupported (what the FTP adapter returns) reaches the UI as a
	// plain action error, not a crash or a silent no-op.
	model := loadedModelOver(t, unsupportedChmodFS{}, unsupportedChmodFS{}, newScriptedEngine())
	model.focus = focusLocal
	model.local.entries = []domain.Entry{{Name: "notes.txt", Kind: domain.EntryFile, Mode: "-rw-r--r--"}}
	model.local.cursor = 0

	model = press(t, model, runes("m"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if !model.statusErr {
		t.Fatalf("an unsupported chmod did not report an error (status=%q)", model.status)
	}
}
