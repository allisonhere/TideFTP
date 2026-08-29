package ui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/vfs"
)

// editMaxBytes caps what the edit flow will pull into a temp file. Editing is
// for configs and scripts, not media.
const editMaxBytes = 5 << 20

// editTimeout bounds the read into, and the write back from, the temp file.
// Larger than listTimeout because a file can be bigger than a listing.
const editTimeout = 60 * time.Second

// pendingEdit tracks a file that has been checked out to a temp path and is
// (about to be) open in the editor.
type pendingEdit struct {
	pane    paneID
	path    string   // the file's path in its pane's filesystem
	name    string   // basename, for status messages
	tmpPath string   // local temp copy handed to the editor
	sum     [32]byte // sha256 of the original contents
}

type editPreparedMsg struct {
	pane    paneID
	path    string
	name    string
	tmpPath string
	sum     [32]byte
	err     error
}

type editorClosedMsg struct{ err error }

type editSavedMsg struct {
	pane    paneID
	name    string
	changed bool
	err     error
}

// startEdit checks out the highlighted file and, once it is on disk, opens it
// in the user's editor. It is bound to `e` in a file pane and to the palette's
// "Edit file" command.
func (m *Model) startEdit() tea.Cmd {
	paneID, ok := m.focusedPaneID()
	if !ok {
		m.setError("focus a file pane first")
		return nil
	}
	if paneID == paneRemote && !m.connected() {
		m.setError("not connected")
		return nil
	}
	pane := m.filePaneByID(paneID)
	entry, found := pane.current()
	if !found || isParentDirEntry(entry) {
		m.setError("highlight a file to edit")
		return nil
	}
	if entry.IsDir() {
		m.setError("cannot edit a directory")
		return nil
	}
	if entry.Size > editMaxBytes {
		m.setError(fmt.Sprintf("%s is too large to edit (%d bytes)", entry.Name, entry.Size))
		return nil
	}
	if _, err := editorArgv(); err != nil {
		m.setError(err.Error())
		return nil
	}
	fs := m.fsByID(paneID)
	path := fs.Child(pane.path, entry.Name)
	m.setStatus("opening " + entry.Name + "…")
	return editPrepareCmd(fs, paneID, path, entry.Name)
}

// editPrepareCmd reads path into a temp file and hashes the original.
func editPrepareCmd(fs vfs.FS, pane paneID, path, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), editTimeout)
		defer cancel()

		data, err := fs.ReadFile(ctx, path)
		if err != nil {
			return editPreparedMsg{pane: pane, path: path, name: name, err: err}
		}
		if len(data) > editMaxBytes {
			return editPreparedMsg{pane: pane, path: path, name: name, err: fmt.Errorf("%s is too large to edit", name)}
		}
		if looksBinary(data) {
			return editPreparedMsg{pane: pane, path: path, name: name, err: fmt.Errorf("%s looks binary", name)}
		}

		tmp, err := os.CreateTemp("", "tideftp-*-"+name)
		if err != nil {
			return editPreparedMsg{pane: pane, path: path, name: name, err: err}
		}
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return editPreparedMsg{pane: pane, path: path, name: name, err: err}
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmp.Name())
			return editPreparedMsg{pane: pane, path: path, name: name, err: err}
		}
		return editPreparedMsg{pane: pane, path: path, name: name, tmpPath: tmp.Name(), sum: sha256.Sum256(data)}
	}
}

// editSaveCmd re-reads the temp file after the editor exits and writes it back
// only when the contents actually changed. The temp file is always removed.
func editSaveCmd(fs vfs.FS, edit pendingEdit) tea.Cmd {
	return func() tea.Msg {
		defer os.Remove(edit.tmpPath)

		data, err := os.ReadFile(edit.tmpPath)
		if err != nil {
			return editSavedMsg{pane: edit.pane, name: edit.name, err: err}
		}
		if sha256.Sum256(data) == edit.sum {
			return editSavedMsg{pane: edit.pane, name: edit.name, changed: false}
		}

		ctx, cancel := context.WithTimeout(context.Background(), editTimeout)
		defer cancel()
		if err := fs.WriteFile(ctx, edit.path, data); err != nil {
			return editSavedMsg{pane: edit.pane, name: edit.name, changed: true, err: err}
		}
		return editSavedMsg{pane: edit.pane, name: edit.name, changed: true}
	}
}

// editorFallbacks are tried, in order, when neither $VISUAL nor $EDITOR is set.
// "editor" is the Debian alternatives symlink; the rest are common installs.
var editorFallbacks = []string{"editor", "nano", "vim", "nvim", "vi", "micro", "hx", "emacs"}

// editorArgv resolves the editor command prefix: $VISUAL, then $EDITOR (either
// may carry flags, e.g. "code -w"), then the first fallback found on PATH. It
// returns an error when nothing usable is found, so startEdit can report that
// before checking a file out.
func editorArgv() ([]string, error) {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		spec := strings.TrimSpace(os.Getenv(env))
		if spec == "" {
			continue
		}
		fields := strings.Fields(spec)
		if _, err := exec.LookPath(fields[0]); err != nil {
			return nil, fmt.Errorf("$%s is %q, which is not on PATH", env, fields[0])
		}
		return fields, nil
	}
	for _, name := range editorFallbacks {
		if path, err := exec.LookPath(name); err == nil {
			return []string{path}, nil
		}
	}
	return nil, fmt.Errorf("no editor found — set $EDITOR (tried %s)", strings.Join(editorFallbacks, ", "))
}

// editorCommand builds the editor invocation for tmpPath.
func editorCommand(tmpPath string) (*exec.Cmd, error) {
	argv, err := editorArgv()
	if err != nil {
		return nil, err
	}
	return exec.Command(argv[0], append(argv[1:], tmpPath)...), nil
}

// looksBinary reports whether the first 8 KB contains a NUL byte.
func looksBinary(data []byte) bool {
	if len(data) > 8192 {
		data = data[:8192]
	}
	return bytes.IndexByte(data, 0) >= 0
}
