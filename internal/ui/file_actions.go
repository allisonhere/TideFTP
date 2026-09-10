package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/vfs"
)

func fileActionLabel(kind fileActionKind) string {
	switch kind {
	case fileActionMkdir:
		return "new folder"
	case fileActionRename:
		return "rename"
	case fileActionRenameForce:
		return "replace"
	case fileActionDelete:
		return "delete"
	case fileActionChmod:
		return "chmod"
	default:
		return "file action"
	}
}

func fileActionDoneLabel(kind fileActionKind) string {
	switch kind {
	case fileActionMkdir:
		return "folder created"
	case fileActionRename, fileActionRenameForce:
		return "renamed"
	case fileActionDelete:
		return "deleted"
	case fileActionChmod:
		return "permissions changed"
	default:
		return "done"
	}
}

// fileActionIsConfirm reports whether kind is a yes/no confirmation rather
// than a text-entry prompt.
func fileActionIsConfirm(kind fileActionKind) bool {
	return kind == fileActionDelete || kind == fileActionRenameForce
}

func (m *Model) focusedMutablePane() (paneID, *filePane, bool) {
	paneID, ok := m.focusedPaneID()
	if !ok {
		m.setError("focus a file pane first")
		return paneLocal, nil, false
	}
	if paneID == paneRemote && !m.connected() {
		m.setError("not connected")
		return paneLocal, nil, false
	}
	return paneID, m.filePaneByID(paneID), true
}

func (m *Model) openMkdirPrompt() {
	paneID, _, ok := m.focusedMutablePane()
	if !ok {
		return
	}
	m.fileAction = &fileActionPrompt{kind: fileActionMkdir, pane: paneID}
	m.overlay = overlayFileAction
	m.setStatus("new folder")
}

func (m *Model) openRenamePrompt() {
	paneID, pane, ok := m.focusedMutablePane()
	if !ok {
		return
	}
	entry, found := pane.current()
	if !found || isParentDirEntry(entry) {
		m.setError("highlight an item to rename")
		return
	}
	m.fileAction = &fileActionPrompt{kind: fileActionRename, pane: paneID, text: entry.Name, cursor: len([]rune(entry.Name)), oldName: entry.Name}
	m.overlay = overlayFileAction
	m.setStatus("rename " + entry.Name)
}

// openDeletePrompt asks the user to confirm a delete. A selection that
// includes a folder is counted first (see beginDeleteScan) so the prompt can
// say whether this is five files or fifty thousand, and so the progress bar
// has a denominator; the overlay opens when that count lands.
func (m *Model) openDeletePrompt() tea.Cmd {
	if m.deleteJob != nil {
		m.setError("a delete is already running")
		return nil
	}
	paneID, pane, ok := m.focusedMutablePane()
	if !ok {
		return nil
	}
	entries := pane.actionEntries()
	if len(entries) == 0 {
		m.setError("highlight or select item(s) to delete")
		return nil
	}
	hasDir := false
	for _, entry := range entries {
		if entry.IsDir() {
			hasDir = true
			break
		}
	}
	if !hasDir {
		// Nothing to walk: the listing already knows how many items are going.
		m.fileAction = &fileActionPrompt{kind: fileActionDelete, pane: paneID, entries: entries}
		m.overlay = overlayFileAction
		m.setStatus(fmt.Sprintf("delete %d item(s)?", len(entries)))
		return nil
	}
	m.setStatus("scanning…")
	return beginDeleteScan(m.fsByID(paneID), pane.path, paneID, entries)
}

func (m *Model) openChmodPrompt() {
	paneID, pane, ok := m.focusedMutablePane()
	if !ok {
		return
	}
	entries := pane.actionEntries()
	if len(entries) == 0 {
		m.setError("highlight or select item(s) to chmod")
		return
	}
	prefill := modeStringToOctal(entries[0].Mode)
	if prefill == "" {
		prefill = "644"
		if entries[0].IsDir() {
			prefill = "755"
		}
	}
	label := entries[0].Name
	if len(entries) > 1 {
		label = fmt.Sprintf("%d items", len(entries))
	}
	m.fileAction = &fileActionPrompt{
		kind:    fileActionChmod,
		pane:    paneID,
		text:    prefill,
		cursor:  len([]rune(prefill)),
		oldName: label,
		entries: entries,
	}
	m.overlay = overlayFileAction
	m.setStatus("chmod " + label)
}

func (m *Model) handleFileActionKey(msg tea.KeyMsg) tea.Cmd {
	if m.fileAction == nil {
		m.overlay = overlayNone
		return nil
	}
	prompt := m.fileAction

	// A confirmation prompt (delete, or replace-on-rename) takes yes/no
	// shortcuts. The mkdir/rename prompts are text fields: only esc and enter
	// are reserved, everything else edits the name (so a folder called
	// "notes" or "query" can actually be typed).
	if fileActionIsConfirm(prompt.kind) {
		switch msg.String() {
		case "esc", "q", "n":
			m.fileAction = nil
			m.overlay = overlayNone
			m.setStatus("cancelled")
		case "enter", "y":
			return m.submitFileAction()
		}
		return nil
	}

	switch msg.String() {
	case "esc":
		m.fileAction = nil
		m.overlay = overlayNone
		m.setStatus("cancelled")
	case "enter":
		return m.submitFileAction()
	case "backspace":
		runes := []rune(prompt.text)
		if prompt.cursor > 0 && prompt.cursor <= len(runes) {
			prompt.text = string(append(runes[:prompt.cursor-1], runes[prompt.cursor:]...))
			prompt.cursor--
		}
	case "delete":
		runes := []rune(prompt.text)
		if prompt.cursor >= 0 && prompt.cursor < len(runes) {
			prompt.text = string(append(runes[:prompt.cursor], runes[prompt.cursor+1:]...))
		}
	case "left":
		prompt.cursor = max(0, prompt.cursor-1)
	case "right":
		prompt.cursor = min(len([]rune(prompt.text)), prompt.cursor+1)
	case "home":
		prompt.cursor = 0
	case "end":
		prompt.cursor = len([]rune(prompt.text))
	case "ctrl+u":
		prompt.text = ""
		prompt.cursor = 0
	default:
		if len(msg.Runes) > 0 {
			runes := []rune(prompt.text)
			cur := min(max(prompt.cursor, 0), len(runes))
			inserted := msg.Runes
			runes = append(runes[:cur], append(inserted, runes[cur:]...)...)
			prompt.text = string(runes)
			prompt.cursor = cur + len(inserted)
		}
	}
	return nil
}

func (m *Model) submitFileAction() tea.Cmd {
	prompt := m.fileAction
	if prompt == nil {
		return nil
	}
	switch prompt.kind {
	case fileActionMkdir, fileActionRename:
		name := strings.TrimSpace(prompt.text)
		if name == "" {
			m.setError(fileActionLabel(prompt.kind) + " name is required")
			return nil
		}
		if !validFileActionName(name) {
			m.setError("name cannot be \".\", \"..\", or contain a path separator")
			return nil
		}
		prompt.text = name
	case fileActionChmod:
		if _, err := parseChmodMode(prompt.text); err != nil {
			m.setError(err.Error())
			return nil
		}
	}
	// The connection can drop between opening this prompt and confirming it,
	// which leaves fsByID handing back a nil interface — calling through it
	// panics and takes the whole app down.
	fs := m.fsByID(prompt.pane)
	if fs == nil {
		m.fileAction = nil
		m.overlay = overlayNone
		m.setError("not connected")
		return nil
	}
	m.fileAction = nil
	m.overlay = overlayNone
	// A delete is the one action that can run for minutes, so it becomes a
	// streamed job with its own progress row instead of a single blocking
	// command that reports nothing until it is finished.
	if prompt.kind == fileActionDelete {
		return m.startDelete(*prompt)
	}
	m.setStatus(fileActionLabel(prompt.kind) + "...")
	return fileActionCmd(fs, m.filePaneByID(prompt.pane).path, *prompt)
}

// validFileActionName rejects names that would resolve outside the current
// directory. mkdir/rename create a single child of the focused pane's path, so
// separators and dot-references are never legitimate here.
func validFileActionName(name string) bool {
	if name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, `/\`+"\x00")
}

func fileActionCmd(fs vfs.FS, base string, prompt fileActionPrompt) tea.Cmd {
	return func() tea.Msg {
		// Every action routed here is a single round trip or a short batch of
		// them, so the tight budget is right: a hung rename, mkdir or chmod
		// surfaces quickly. Delete does not come through here — see
		// startDeleteJob, which has no whole-walk deadline at all.
		ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
		defer cancel()
		var err error
		newName := strings.TrimSpace(prompt.text)
		switch prompt.kind {
		case fileActionMkdir:
			err = fs.Mkdir(ctx, fs.Child(base, newName))
		case fileActionRename:
			err = fs.Rename(ctx, fs.Child(base, prompt.oldName), fs.Child(base, newName))
		case fileActionRenameForce:
			if err = fs.Remove(ctx, fs.Child(base, newName)); err == nil {
				err = fs.Rename(ctx, fs.Child(base, prompt.oldName), fs.Child(base, newName))
			}
		case fileActionChmod:
			mode, perr := parseChmodMode(prompt.text)
			if perr != nil {
				err = perr
				break
			}
			for _, entry := range prompt.entries {
				if isParentDirEntry(entry) {
					continue
				}
				if err = fs.Chmod(ctx, fs.Child(base, entry.Name), mode); err != nil {
					break
				}
			}
		}
		return fileActionMsg{kind: prompt.kind, pane: prompt.pane, err: err, oldName: prompt.oldName, newName: newName}
	}
}
