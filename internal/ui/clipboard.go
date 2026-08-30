package ui

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// clipboardCopiedMsg reports the result of one copy attempt. via names the
// mechanism that took the text so the status line can say where it went —
// "the clipboard" means something different over SSH than it does locally,
// and the user is the one who has to know which they got.
type clipboardCopiedMsg struct {
	count int
	via   string
	err   error
}

// clipboardTools are the local clipboard commands to try, in order, with the
// arguments that make each one read the text from stdin and exit. wl-copy is
// first because Wayland is the common case on this app's home platform;
// clip.exe catches WSL.
var clipboardTools = []struct {
	name string
	args []string
}{
	{"wl-copy", nil},
	{"pbcopy", nil},
	{"xclip", []string{"-selection", "clipboard"}},
	{"xsel", []string{"--clipboard", "--input"}},
	{"clip.exe", nil},
}

// copySelectedPaths copies the focused pane's selection — or, with nothing
// selected, the highlighted entry — to the clipboard as full paths, one per
// line. It is bound to `y` in a file pane and to the palette's "Copy path"
// command.
func (m *Model) copySelectedPaths() tea.Cmd {
	paths, ok := m.selectedPaths()
	if !ok {
		return nil
	}
	return copyToClipboardCmd(strings.Join(paths, "\n"), len(paths))
}

// selectedPaths resolves the focused pane's selection into full paths, and
// reports the reason it could not as a status. It is separate from the copy
// itself so what gets copied can be tested without a clipboard.
func (m *Model) selectedPaths() ([]string, bool) {
	paneID, ok := m.focusedPaneID()
	if !ok {
		m.setError("focus a file pane first")
		return nil, false
	}
	pane := m.filePaneByID(paneID)
	entries := pane.actionEntries()
	if len(entries) == 0 {
		m.setError("nothing selected or highlighted")
		return nil, false
	}
	fs := m.fsByID(paneID)
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		paths = append(paths, fs.Child(pane.path, entry.Name))
	}
	return paths, true
}

// copyToClipboardCmd puts text on the clipboard off the UI goroutine — a
// clipboard helper is a process to spawn, and OSC 52 is a write to the
// terminal, neither of which belongs in Update.
func copyToClipboardCmd(text string, count int) tea.Cmd {
	return func() tea.Msg {
		via, err := copyToClipboard(text)
		return clipboardCopiedMsg{count: count, via: via, err: err}
	}
}

// copyToClipboard writes text to the clipboard and names how it got there.
//
// Over SSH it always uses OSC 52, which asks the terminal emulator the user
// is actually sitting in front of to take the text. A clipboard helper
// installed on the far end would put the paths on the *server's* clipboard,
// which is never what someone copying a path wants. Locally a helper is
// preferred, because it works in terminals that ship with OSC 52 disabled,
// and OSC 52 remains the fallback when no helper is installed.
func copyToClipboard(text string) (string, error) {
	name, path, args := clipboardTool()
	if path == "" {
		return name, writeOSC52(text)
	}
	cmd := exec.Command(path, args...)
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return name, fmt.Errorf("%s: %w", name, err)
	}
	return name, nil
}

// clipboardTool decides which mechanism copyToClipboard should use: the
// name to report, and the executable and arguments to run — an empty path
// meaning OSC 52. Keeping the choice separate from the doing is what lets
// it be tested without putting anything on a real clipboard.
func clipboardTool() (name, path string, args []string) {
	if isRemoteSession() {
		return "osc52", "", nil
	}
	for _, tool := range clipboardTools {
		found, err := exec.LookPath(tool.name)
		if err != nil {
			continue
		}
		return tool.name, found, tool.args
	}
	return "osc52", "", nil
}

// isRemoteSession reports whether this process is running over SSH.
func isRemoteSession() bool {
	return os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" || os.Getenv("SSH_CLIENT") != ""
}

// writeOSC52 asks the terminal to set its clipboard, via the OSC 52 escape
// sequence. It writes straight to the terminal rather than going through
// Bubble Tea, which has no clipboard command of its own; the sequence
// produces no visible output, and the next frame repaints over it either
// way.
func writeOSC52(text string) error {
	_, err := io.WriteString(os.Stdout, osc52Sequence(text))
	return err
}

// osc52Sequence builds the escape sequence that sets the terminal's
// clipboard. BEL rather than ST terminates it: that is what tmux, screen,
// and every terminal with OSC 52 support accept.
func osc52Sequence(text string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\a"
}

// applyClipboardCopied turns a copy result into a status line.
func (m *Model) applyClipboardCopied(msg clipboardCopiedMsg) {
	if msg.err != nil {
		m.setError(fmt.Sprintf("copy path: %v", msg.err))
		return
	}
	noun := "path"
	if msg.count != 1 {
		noun = "paths"
	}
	m.setStatus(fmt.Sprintf("copied %d %s (%s)", msg.count, noun, msg.via))
}
