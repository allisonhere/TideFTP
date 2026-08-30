package ui

import (
	"context"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
	"tideftp/internal/localfs"
	"tideftp/internal/vfs"
)

// remoteAt focuses the remote pane on the named entry of dirPath, the setup
// every preview test starts from.
func remoteAt(t *testing.T, model Model, dirPath, name string) Model {
	t.Helper()
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, dirPath))
	for i, entry := range model.remote.entries {
		if entry.Name == name {
			model.remote.cursor = i
			return model
		}
	}
	t.Fatalf("%s not listed in %s", name, dirPath)
	return model
}

func TestPreviewShowsTheFileContents(t *testing.T) {
	model := loadedModelOver(t, localfs.New(), fakefs.NewRemote(), newScriptedEngine())
	model = remoteAt(t, model, "/public_html", "robots.txt")

	model = press(t, model, runes("v"))

	if model.overlay != overlayPreview || model.preview == nil {
		t.Fatalf("pressing v did not open a preview; overlay=%v status=%q", model.overlay, model.status)
	}
	if model.preview.name != "robots.txt" {
		t.Fatalf("preview.name = %q, want robots.txt", model.preview.name)
	}
	if model.preview.hex {
		t.Fatalf("a text file must open as text, not as a hexdump")
	}
	lines := model.preview.lines(80)
	if len(lines) != 2 || lines[0] != "User-agent: *" || lines[1] != "Disallow:" {
		t.Fatalf("preview lines = %q, want the file's two lines with no trailing blank", lines)
	}
}

func TestPreviewTogglesToHex(t *testing.T) {
	model := loadedModelOver(t, localfs.New(), fakefs.NewRemote(), newScriptedEngine())
	model = remoteAt(t, model, "/public_html", "robots.txt")
	model = press(t, model, runes("v"))

	model = press(t, model, runes("x"))

	if !model.preview.hex {
		t.Fatalf("x did not switch the preview to hex")
	}
	first := model.preview.lines(120)[0]
	// "User-agent: *\n" — offset, the bytes, then the printable gutter.
	if !strings.HasPrefix(first, "00000000  55 73 65 72") || !strings.Contains(first, "|User-agent: *.") {
		t.Fatalf("hexdump line = %q, want an offset, hex bytes, and an ASCII gutter", first)
	}
}

// binaryFS serves one file whose body the test chooses, which fakefs's fixed
// tree cannot do for a binary or an oversized body.
type binaryFS struct {
	vfs.FS
	body []byte
	// name is what the single entry is called, which is also what the
	// highlighter matches a lexer against. Empty defaults to a name with no
	// language attached to it.
	name string
}

func (f *binaryFS) List(_ context.Context, _ string, _ bool) ([]domain.Entry, error) {
	name := f.name
	if name == "" {
		name = "blob.bin"
	}
	return []domain.Entry{{Name: name, Kind: domain.EntryFile, Size: int64(len(f.body))}}, nil
}
func (f *binaryFS) Open(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(f.body))), nil
}

func TestPreviewOpensBinaryContentAsHex(t *testing.T) {
	remote := &binaryFS{FS: fakefs.NewRemote(), body: []byte{0x7f, 'E', 'L', 'F', 0x00, 0x01, 0x02}}
	model := loadedModelOver(t, localfs.New(), remote, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/"))

	model = press(t, model, runes("v"))

	if model.preview == nil {
		t.Fatalf("no preview opened; status=%q", model.status)
	}
	if !model.preview.binary || !model.preview.hex {
		t.Fatalf("binary=%v hex=%v, want a binary file to open as a hexdump", model.preview.binary, model.preview.hex)
	}
}

func TestPreviewReadsOnlyItsCapAndSaysSo(t *testing.T) {
	remote := &binaryFS{FS: fakefs.NewRemote(), body: []byte(strings.Repeat("a", previewMaxBytes+4096))}
	model := loadedModelOver(t, localfs.New(), remote, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/"))

	model = press(t, model, runes("v"))

	if model.preview == nil {
		t.Fatalf("no preview opened; status=%q", model.status)
	}
	if len(model.preview.data) != previewMaxBytes {
		t.Fatalf("read %d bytes, want the preview capped at %d", len(model.preview.data), previewMaxBytes)
	}
	if !model.preview.truncated {
		t.Fatalf("a file longer than the cap must be reported as truncated")
	}
	if !strings.Contains(model.preview.summary(), "first ") {
		t.Fatalf("summary = %q, want it to say only the head was read", model.preview.summary())
	}
}

func TestPreviewNamesTheLanguageItHighlighted(t *testing.T) {
	remote := &binaryFS{FS: fakefs.NewRemote(), body: []byte("server {\n    listen 443;\n}\n"), name: "nginx.conf"}
	model := loadedModelOver(t, localfs.New(), remote, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/"))

	model = press(t, model, runes("v"))

	if model.preview == nil {
		t.Fatalf("no preview opened; status=%q", model.status)
	}
	if model.preview.language == "" {
		t.Fatalf("nginx.conf previewed with no language recognised")
	}
	if !strings.Contains(model.preview.summary(), model.preview.language) {
		t.Fatalf("summary = %q, want it to name the language", model.preview.summary())
	}
}

// A hexdump is not written in any language, so the header must not claim one.
func TestPreviewHexHeaderNamesNoLanguage(t *testing.T) {
	body := []byte("package main\n\x00\x01binary tail")
	preview := newPreviewState("main.go", "/srv/main.go", int64(len(body)), body, false)

	if !preview.hex {
		t.Fatalf("content with a NUL should open as a hexdump")
	}
	if strings.Contains(preview.summary(), "Go") {
		t.Fatalf("summary = %q, want no language on a hexdump", preview.summary())
	}
}

func TestPreviewRejectsADirectory(t *testing.T) {
	model := loadedModelOver(t, localfs.New(), fakefs.NewRemote(), newScriptedEngine())
	model = remoteAt(t, model, "/", "public_html")

	model = press(t, model, runes("v"))

	if model.overlay == overlayPreview {
		t.Fatalf("a directory must not open a preview")
	}
	if !model.statusErr || !strings.Contains(model.status, "directory") {
		t.Fatalf("status = %q, want an error naming the directory", model.status)
	}
}

func TestPreviewEscapeSequencesAreNeutralised(t *testing.T) {
	lines := textLines([]byte("plain\x1b[31mred\x07\ttabbed"), 80)
	if len(lines) != 1 {
		t.Fatalf("lines = %q, want one", lines)
	}
	if strings.ContainsRune(lines[0], '\x1b') || strings.ContainsRune(lines[0], '\a') {
		t.Fatalf("line = %q, still carries control characters a terminal would act on", lines[0])
	}
	if !strings.Contains(lines[0], strings.Repeat(" ", previewTabWidth)+"tabbed") {
		t.Fatalf("line = %q, want the tab expanded to spaces", lines[0])
	}
}

func TestPreviewScrollsWithinItsLines(t *testing.T) {
	body := strings.Repeat("line\n", 200)
	remote := &binaryFS{FS: fakefs.NewRemote(), body: []byte(body)}
	model := loadedModelOver(t, localfs.New(), remote, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/"))
	model = press(t, model, runes("v"))

	model = press(t, model, tea.KeyMsg{Type: tea.KeyPgDown})
	if model.preview.offset != model.previewVisibleRows() {
		t.Fatalf("offset = %d after pgdown, want one page (%d)", model.preview.offset, model.previewVisibleRows())
	}

	model = press(t, model, runes("G"))
	total := len(model.preview.lines(model.previewWidth() - 4))
	if want := total - model.previewVisibleRows(); model.preview.offset != want {
		t.Fatalf("offset = %d at the end, want %d", model.preview.offset, want)
	}

	model = press(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.overlay != overlayNone || model.preview != nil {
		t.Fatalf("esc did not close the preview: overlay=%v preview=%+v", model.overlay, model.preview)
	}
}
