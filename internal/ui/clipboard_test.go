package ui

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"tideftp/internal/fakefs"
	"tideftp/internal/localfs"
)

// These tests deliberately never call copyToClipboard: it would put text on
// the machine's real clipboard, which a test run has no business doing. What
// gets copied (selectedPaths) and how it would be copied (clipboardTool,
// osc52Sequence) are tested separately, which is why they are separate.

func TestCopyPathUsesTheHighlightedEntry(t *testing.T) {
	model := loadedModelOver(t, localfs.New(), fakefs.NewRemote(), newScriptedEngine())
	model = remoteAt(t, model, "/public_html", "index.html")

	paths, ok := model.selectedPaths()

	if !ok {
		t.Fatalf("no path resolved; status=%q", model.status)
	}
	if len(paths) != 1 || paths[0] != "/public_html/index.html" {
		t.Fatalf("paths = %q, want the highlighted entry's full remote path", paths)
	}
}

func TestCopyPathUsesTheWholeSelection(t *testing.T) {
	model := loadedModelOver(t, localfs.New(), fakefs.NewRemote(), newScriptedEngine())
	model = remoteAt(t, model, "/public_html", "index.html")
	model.remote.selected = map[string]bool{"index.html": true, "app.css": true}

	paths, ok := model.selectedPaths()

	if !ok {
		t.Fatalf("no paths resolved; status=%q", model.status)
	}
	if len(paths) != 2 {
		t.Fatalf("paths = %q, want both selected entries", paths)
	}
	for _, want := range []string{"/public_html/app.css", "/public_html/index.html"} {
		if !slices.Contains(paths, want) {
			t.Fatalf("paths = %q, missing %q", paths, want)
		}
	}
}

func TestCopyPathReportsAnEmptySelection(t *testing.T) {
	model := loadedModelOver(t, localfs.New(), fakefs.NewRemote(), newScriptedEngine())
	model.focus = focusRemote
	model.remote.entries = nil

	if _, ok := model.selectedPaths(); ok {
		t.Fatalf("an empty pane resolved a path out of nothing")
	}
	if !model.statusErr {
		t.Fatalf("status = %q, want an error", model.status)
	}
}

func TestCopyPathIsRefusedFromTheTransferPane(t *testing.T) {
	model := loadedModelOver(t, localfs.New(), fakefs.NewRemote(), newScriptedEngine())
	model.focus = focusQueue

	if _, ok := model.selectedPaths(); ok {
		t.Fatalf("the transfers pane has no file paths to copy")
	}
}

func TestClipboardOverSSHAlwaysUsesOSC52(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "10.0.0.2 51000 10.0.0.9 22")

	name, path, _ := clipboardTool()

	if name != "osc52" || path != "" {
		// A helper on the far end would set the *server's* clipboard.
		t.Fatalf("tool = %q (%q) over SSH, want osc52 with no local helper", name, path)
	}
}

func TestOSC52SequenceCarriesTheEncodedText(t *testing.T) {
	seq := osc52Sequence("/public_html/index.html")

	if !strings.HasPrefix(seq, "\x1b]52;c;") || !strings.HasSuffix(seq, "\a") {
		t.Fatalf("sequence = %q, want an OSC 52 clipboard set terminated by BEL", seq)
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b]52;c;"), "\a")
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("payload is not base64: %v", err)
	}
	if string(decoded) != "/public_html/index.html" {
		t.Fatalf("decoded = %q, want the path", decoded)
	}
}

func TestClipboardStatusNamesTheMechanism(t *testing.T) {
	var model Model

	model.applyClipboardCopied(clipboardCopiedMsg{count: 2, via: "wl-copy"})

	if !strings.Contains(model.status, "2 paths") || !strings.Contains(model.status, "wl-copy") {
		t.Fatalf("status = %q, want the count and where it went", model.status)
	}
}
