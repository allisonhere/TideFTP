package ui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/vfs"
)

// previewMaxBytes is how much of a file the preview reads. Unlike the edit
// flow, which refuses a file it cannot check out whole, preview reads a
// prefix and says so: peeking at the head of a 4 GB log is exactly what this
// is for, and vfs.FS.Open makes reading only the first chunk of it possible.
const previewMaxBytes = 128 << 10

// previewTimeout bounds opening the file and reading the prefix. Shorter
// than editTimeout because far less is being moved.
const previewTimeout = 30 * time.Second

// previewTabWidth is what a tab expands to. Preview lines are truncated to
// the overlay width, so a real tab stop would shift text off the right edge
// for no benefit over a fixed indent.
const previewTabWidth = 4

// previewState is the file currently open in overlayPreview.
type previewState struct {
	name string
	path string
	// size is the file's full length as the listing reported it, which is
	// what makes "showing the first 128 KB of 4.2 GB" sayable at all —
	// data alone cannot tell you what you are missing.
	size      int64
	data      []byte
	truncated bool // the file is longer than data
	binary    bool // data contains a NUL in its first 8 KB (see looksBinary)
	hex       bool // render as a hexdump rather than as text
	offset    int
	// spans is the text view, already tokenised and coloured (see
	// highlight.go), one entry per line. Built once when the file is read
	// rather than per frame: lexing 128 KB is not work to repeat on every
	// keypress, and it must not happen on the Update goroutine at all.
	spans [][]previewSpan
	// language is what the highlighter recognised, empty when it recognised
	// nothing. Shown in the header so a file that came out uncoloured says
	// why, rather than leaving the user wondering if the feature is broken.
	language string
}

// newPreviewState assembles a preview, including the syntax pass. It runs on
// a command goroutine, never in Update.
func newPreviewState(name, path string, size int64, data []byte, truncated bool) previewState {
	binary := looksBinary(data)
	// Binary content is lexed as nothing: it has no syntax to find, and
	// handing a lexer a blob of bytes is only a way to spend time. It opens
	// as a hexdump anyway; the plain spans are what the x toggle falls back
	// to.
	lexName := name
	if binary {
		lexName = ""
	}
	spans, language := highlight(lexName, data)
	return previewState{
		name:      name,
		path:      path,
		size:      size,
		data:      data,
		truncated: truncated,
		binary:    binary,
		hex:       binary,
		spans:     spans,
		language:  language,
	}
}

type previewLoadedMsg struct {
	name  string
	state previewState
	err   error
}

// startPreview reads the head of the highlighted file and opens it in
// overlayPreview. It is bound to `v` in a file pane and to the palette's
// "Preview file" command.
func (m *Model) startPreview() tea.Cmd {
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
		m.setError("highlight a file to preview")
		return nil
	}
	if entry.IsDir() {
		m.setError("cannot preview a directory")
		return nil
	}
	fs := m.fsByID(paneID)
	m.setStatus("reading " + entry.Name + "…")
	return previewCmd(fs, fs.Child(pane.path, entry.Name), entry.Name, entry.Size)
}

// previewCmd reads at most previewMaxBytes from path. It asks for one byte
// more than it will keep, which is how it knows whether there was more file
// to come without trusting the listing's size — an FTP LIST parser that
// could not work out a size reports 0, and a preview that claimed such a
// file was complete would be lying about the one thing it exists to show.
func previewCmd(fs vfs.FS, path, name string, size int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), previewTimeout)
		defer cancel()

		reader, err := fs.Open(ctx, path)
		if err != nil {
			return previewLoadedMsg{name: name, err: err}
		}
		defer reader.Close()

		data, err := io.ReadAll(io.LimitReader(reader, previewMaxBytes+1))
		if err != nil {
			return previewLoadedMsg{name: name, err: err}
		}
		truncated := len(data) > previewMaxBytes
		if truncated {
			data = data[:previewMaxBytes]
		}
		return previewLoadedMsg{name: name, state: newPreviewState(name, path, size, data, truncated)}
	}
}

// openPreview installs a loaded preview. A file that looks binary opens as a
// hexdump: rendering its bytes as text would fill the overlay with
// replacement characters and show nothing useful.
//
// A read of a large remote file takes long enough that the user may have
// opened something else while waiting — the connect form, the palette. The
// preview does not shoulder its way in front of that; it is discarded, and
// `v` will read it again.
func (m *Model) openPreview(msg previewLoadedMsg) {
	if m.overlay != overlayNone {
		return
	}
	state := msg.state
	m.preview = &state
	m.overlay = overlayPreview
	m.setStatus("previewing " + msg.name)
}

// lines renders the preview's current mode into plain display rows, each
// truncated to width. It is what the scroll maths counts and what a golden
// file records; the coloured rendering (spanRows) walks the same lines.
func (p previewState) lines(width int) []string {
	if p.hex {
		return hexdumpLines(p.data, width)
	}
	rows := make([]string, len(p.spans))
	for i, spans := range p.spans {
		rows[i] = short(spanText(spans), width)
	}
	return rows
}

// textLines is the no-lexer rendering of data, the shape every preview had
// before highlighting existed.
func textLines(data []byte, width int) []string {
	spans := spanLines("", data)
	rows := make([]string, len(spans))
	for i, line := range spans {
		rows[i] = short(spanText(line), width)
	}
	return rows
}

// hexdumpLines renders data in the classic 16-bytes-per-row layout: offset,
// hex bytes split into two groups of eight, then the printable ASCII gutter.
func hexdumpLines(data []byte, width int) []string {
	const perLine = 16
	lines := make([]string, 0, len(data)/perLine+1)
	for start := 0; start < len(data); start += perLine {
		end := min(start+perLine, len(data))
		chunk := data[start:end]

		var hex, ascii strings.Builder
		for i := range perLine {
			if i == perLine/2 {
				hex.WriteByte(' ')
			}
			if i < len(chunk) {
				fmt.Fprintf(&hex, "%02x ", chunk[i])
				if chunk[i] >= 0x20 && chunk[i] < 0x7f {
					ascii.WriteByte(chunk[i])
				} else {
					ascii.WriteByte('.')
				}
			} else {
				hex.WriteString("   ")
			}
		}
		lines = append(lines, short(fmt.Sprintf("%08x  %s |%s|", start, hex.String(), ascii.String()), width))
	}
	if len(lines) == 0 {
		lines = append(lines, "(empty)")
	}
	return lines
}

// previewVisibleRows is how many file rows the overlay shows at the current
// terminal height, the window previewOffset scrolls within.
func (m Model) previewVisibleRows() int {
	return max(5, min(28, m.height-12))
}

// previewWidth is how wide the preview overlay is drawn. A hexdump has a
// fixed natural width, so the overlay is sized to fit one exactly rather
// than wrapping it; text gets whatever the terminal allows.
func (m Model) previewWidth() int {
	const hexdumpWidth = 78
	if m.preview != nil && m.preview.hex {
		return min(hexdumpWidth+6, max(40, m.width-4))
	}
	return min(100, max(40, m.width-8))
}

func (m *Model) clampPreviewOffset() {
	if m.preview == nil {
		return
	}
	total := len(m.preview.lines(m.previewWidth() - 4))
	m.preview.offset = min(max(0, m.preview.offset), max(0, total-m.previewVisibleRows()))
}

// handlePreviewKey routes keys while the preview overlay is open. It is a
// read-only viewer, so every key either scrolls, switches the render mode,
// or closes it.
func (m *Model) handlePreviewKey(msg tea.KeyMsg) tea.Cmd {
	if m.preview == nil {
		m.overlay = overlayNone
		return nil
	}
	switch msg.String() {
	case "esc", "q", "v":
		m.overlay = overlayNone
		m.preview = nil
		m.setStatus("preview closed")
	case "up", "k":
		m.preview.offset--
	case "down", "j":
		m.preview.offset++
	case "pgup":
		m.preview.offset -= m.previewVisibleRows()
	case "pgdown":
		m.preview.offset += m.previewVisibleRows()
	case "home", "g":
		m.preview.offset = 0
	case "end", "G":
		m.preview.offset = len(m.preview.lines(m.previewWidth() - 4))
	case "x":
		m.preview.hex = !m.preview.hex
		m.preview.offset = 0
		if m.preview.hex {
			m.setStatus("preview: hex")
		} else {
			m.setStatus("preview: text")
		}
	}
	m.clampPreviewOffset()
	return nil
}

// previewSummary is the overlay's meta line: how much of the file is on
// screen, and how much of it was read at all.
func (p previewState) summary() string {
	mode := "text"
	if p.hex {
		mode = "hex"
	} else if p.language != "" {
		// Only in text mode: a hexdump is not written in any language, and
		// saying it is would be worse than saying nothing.
		mode += " · " + p.language
	}
	if p.truncated {
		return fmt.Sprintf("%s · %s · first %s of %s", mode, p.path, formatSize(int64(len(p.data))), formatSize(p.size))
	}
	return fmt.Sprintf("%s · %s · %s", mode, p.path, formatSize(int64(len(p.data))))
}
