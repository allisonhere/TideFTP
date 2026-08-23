package ui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/session"
)

// connectField names one editable field in the connect form, in display order.
type connectField int

const (
	connectFieldProtocol connectField = iota
	connectFieldHost
	connectFieldPort
	connectFieldUsername
	connectFieldPath
	connectFieldCount
)

// connectProtocols is the protocol picker's cycle order.
var connectProtocols = []string{"sftp", "ftp", "ftps"}

// connectFormValue holds the connect form's field values. Protocol is an index
// into connectProtocols; the rest are free text.
type connectFormValue struct {
	protocol int
	host     string
	port     string
	username string
	path     string
}

func protocolIndex(protocol string) int {
	for i, p := range connectProtocols {
		if p == protocol {
			return i
		}
	}
	return 0
}

// openConnectForm shows the connect form, prefilled from the current target
// (or the first known one) so it is never blank.
func (m *Model) openConnectForm() {
	src := m.target
	if src.Host == "" && len(m.targets) > 0 {
		src = m.targets[0]
	}
	m.connectForm = connectFormValue{
		protocol: protocolIndex(src.Protocol),
		host:     src.Host,
		username: src.User,
		path:     src.StartPath,
	}
	if src.Port != 0 {
		m.connectForm.port = strconv.Itoa(src.Port)
	}
	m.connectField = connectFieldProtocol
	m.connectCursor = 0
	m.overlay = overlayConnect
}

func connectFieldLabel(field connectField) string {
	switch field {
	case connectFieldProtocol:
		return "Protocol"
	case connectFieldHost:
		return "Host"
	case connectFieldPort:
		return "Port"
	case connectFieldUsername:
		return "Username"
	case connectFieldPath:
		return "Path"
	}
	return ""
}

func (m Model) connectFieldValue(field connectField) string {
	switch field {
	case connectFieldProtocol:
		return connectProtocols[m.connectForm.protocol]
	case connectFieldHost:
		return m.connectForm.host
	case connectFieldPort:
		return m.connectForm.port
	case connectFieldUsername:
		return m.connectForm.username
	case connectFieldPath:
		return m.connectForm.path
	}
	return ""
}

func (m *Model) setConnectFieldValue(field connectField, value string) {
	switch field {
	case connectFieldHost:
		m.connectForm.host = value
	case connectFieldPort:
		m.connectForm.port = value
	case connectFieldUsername:
		m.connectForm.username = value
	case connectFieldPath:
		m.connectForm.path = value
	}
}

// connectChoiceField reports whether the given field is a cycled picker rather
// than free text.
func connectChoiceField(field connectField) bool {
	return field == connectFieldProtocol
}

// connectFieldDisplay returns the value shown for field, with a caret inserted
// when it is the focused free-text field.
func (m Model) connectFieldDisplay(field connectField) string {
	value := m.connectFieldValue(field)
	if field == m.connectField && !connectChoiceField(field) {
		runes := []rune(value)
		cur := min(max(m.connectCursor, 0), len(runes))
		runes = append(runes[:cur], append([]rune{'|'}, runes[cur:]...)...)
		return string(runes)
	}
	return value
}

func (m *Model) moveConnectField(delta int) {
	n := int(connectFieldCount)
	m.connectField = connectField((int(m.connectField) + delta + n) % n)
	m.connectCursor = len([]rune(m.connectFieldValue(m.connectField)))
}

func (m *Model) moveConnectCursor(delta int) {
	runes := []rune(m.connectFieldValue(m.connectField))
	m.connectCursor = min(max(m.connectCursor+delta, 0), len(runes))
}

func (m *Model) cycleConnectChoice(delta int) {
	n := len(connectProtocols)
	m.connectForm.protocol = (m.connectForm.protocol + delta + n) % n
}

func (m *Model) editConnectField(s string) {
	runes := []rune(m.connectFieldValue(m.connectField))
	cur := min(max(m.connectCursor, 0), len(runes))
	inserted := []rune(s)
	runes = append(runes[:cur], append(inserted, runes[cur:]...)...)
	m.setConnectFieldValue(m.connectField, string(runes))
	m.connectCursor = cur + len(inserted)
}

func (m *Model) editConnectFieldBackspace() {
	if connectChoiceField(m.connectField) {
		return
	}
	runes := []rune(m.connectFieldValue(m.connectField))
	cur := min(max(m.connectCursor, 0), len(runes))
	if cur == 0 {
		return
	}
	runes = append(runes[:cur-1], runes[cur:]...)
	m.setConnectFieldValue(m.connectField, string(runes))
	m.connectCursor = cur - 1
}

func (m *Model) editConnectFieldDelete() {
	if connectChoiceField(m.connectField) {
		return
	}
	runes := []rune(m.connectFieldValue(m.connectField))
	cur := min(max(m.connectCursor, 0), len(runes))
	if cur >= len(runes) {
		return
	}
	runes = append(runes[:cur], runes[cur+1:]...)
	m.setConnectFieldValue(m.connectField, string(runes))
}

// connectFromForm validates the form and dials it. Validation failures leave
// the overlay open and show the reason; success closes it and connects.
func (m *Model) connectFromForm() tea.Cmd {
	host := strings.TrimSpace(m.connectForm.host)
	if host == "" {
		m.setError("host is required")
		return nil
	}
	port := 0
	if p := strings.TrimSpace(m.connectForm.port); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			m.setError("port must be a number 1-65535")
			return nil
		}
		port = n
	}
	target := session.Target{
		Protocol:  connectProtocols[m.connectForm.protocol],
		Host:      host,
		Port:      port,
		User:      strings.TrimSpace(m.connectForm.username),
		StartPath: strings.TrimSpace(m.connectForm.path),
	}
	m.overlay = overlayNone
	return m.connect(target)
}

// handleConnectKey routes keys while the connect overlay is open, mirroring
// whatthedock's form editing: arrows/tab move between fields, h/l cycle the
// picker or move the caret in free text, and ctrl/alt+enter connects.
func (m *Model) handleConnectKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q":
		m.overlay = overlayNone
		m.setStatus("cancelled")
	case "up":
		m.moveConnectField(-1)
	case "down", "tab":
		m.moveConnectField(1)
	case "shift+tab":
		m.moveConnectField(-1)
	case "k":
		if connectChoiceField(m.connectField) {
			m.moveConnectField(-1)
			return nil
		}
		m.editConnectField("k")
	case "j":
		if connectChoiceField(m.connectField) {
			m.moveConnectField(1)
			return nil
		}
		m.editConnectField("j")
	case "h":
		if connectChoiceField(m.connectField) {
			m.cycleConnectChoice(-1)
			return nil
		}
		m.editConnectField("h")
	case "l":
		if connectChoiceField(m.connectField) {
			m.cycleConnectChoice(1)
			return nil
		}
		m.editConnectField("l")
	case "left":
		if connectChoiceField(m.connectField) {
			m.cycleConnectChoice(-1)
		} else {
			m.moveConnectCursor(-1)
		}
	case "right":
		if connectChoiceField(m.connectField) {
			m.cycleConnectChoice(1)
		} else {
			m.moveConnectCursor(1)
		}
	case "enter":
		if connectChoiceField(m.connectField) {
			m.cycleConnectChoice(1)
			return nil
		}
		m.moveConnectField(1)
	case "ctrl+enter", "alt+enter":
		return m.connectFromForm()
	case "ctrl+d":
		if m.conn != nil {
			m.overlay = overlayNone
			return m.disconnect()
		}
	case "backspace":
		m.editConnectFieldBackspace()
	case "delete":
		m.editConnectFieldDelete()
	case "home", "ctrl+a":
		m.connectCursor = 0
	case "end", "ctrl+e":
		m.connectCursor = len([]rune(m.connectFieldValue(m.connectField)))
	case "ctrl+u":
		if !connectChoiceField(m.connectField) {
			m.setConnectFieldValue(m.connectField, "")
			m.connectCursor = 0
		}
	default:
		if len(msg.Runes) > 0 && !connectChoiceField(m.connectField) {
			m.editConnectField(string(msg.Runes))
		}
	}
	return nil
}
