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
	connectFieldProfile connectField = iota
	connectFieldName
	connectFieldProtocol
	connectFieldHost
	connectFieldPort
	connectFieldUsername
	connectFieldAuth
	connectFieldPassword
	connectFieldPath
	connectFieldCount
)

// connectProtocols is the protocol picker's cycle order.
var connectProtocols = []string{"sftp", "ftp", "ftps"}

// connectAuthChoices is the Auth picker's cycle order. It only applies to
// SFTP — FTP and FTPS have no other way to authenticate, so they force
// "password" without showing the field at all; see connectAuthMode.
var connectAuthChoices = []string{"agent/key", "password"}

// connectFormValue holds the connect form's field values. Protocol is an index
// into connectProtocols, profile is an index into the model's profile choices
// (0 is "new", the rest are saved profiles); the remaining fields, including
// name, are free text.
//
// fresh marks a free-text field as still showing what it was prefilled with —
// from the current target or a loaded profile — rather than something the
// user typed. The first character typed into a fresh field replaces its
// value instead of being inserted into it, the way selecting a form field's
// whole text before typing over it would; any other edit (backspace, delete,
// ctrl+u) marks it not fresh, so typing again only ever inserts.
type connectFormValue struct {
	profile  int
	name     string
	protocol int
	host     string
	port     string
	username string
	auth     int
	password string
	path     string
	fresh    [connectFieldCount]bool
}

// connectAuthMode reports how the form will authenticate: always "password"
// for FTP/FTPS, since they have no other method, or whichever the Auth field
// is set to for SFTP.
func (m Model) connectAuthMode() string {
	if connectProtocols[m.connectForm.protocol] != "sftp" {
		return "password"
	}
	return connectAuthChoices[m.connectForm.auth]
}

// connectFieldVisible reports whether field is shown and reachable in the
// form's current state. Auth only matters for SFTP, the only protocol with
// more than one way to authenticate; Password only matters when the resolved
// auth mode actually uses one.
func (m Model) connectFieldVisible(field connectField) bool {
	switch field {
	case connectFieldAuth:
		return connectProtocols[m.connectForm.protocol] == "sftp"
	case connectFieldPassword:
		return m.connectAuthMode() == "password"
	default:
		return true
	}
}

// credentialsFromForm builds what Dial needs to authenticate. It never
// touches session.Target — credentials are deliberately not part of what a
// profile can persist. PasswordOnly is set only for SFTP, where choosing
// "password" explicitly means skipping the agent and key files rather than
// merely offering Password as a fallback after them.
func (m Model) credentialsFromForm() session.Credentials {
	if m.connectAuthMode() != "password" {
		return session.Credentials{}
	}
	return session.Credentials{
		Password:     m.connectForm.password,
		PasswordOnly: connectProtocols[m.connectForm.protocol] == "sftp",
	}
}

// connectProfileNew is the label shown when the Profile field points at no
// saved profile — the form's values are not tied to one.
const connectProfileNew = "(new)"

// connectProfileChoices lists the Profile field's cycle order: "(new)" first,
// then each saved profile by its label.
func (m Model) connectProfileChoices() []string {
	choices := make([]string, 0, len(m.profiles)+1)
	choices = append(choices, connectProfileNew)
	for _, p := range m.profiles {
		choices = append(choices, p.Label())
	}
	return choices
}

// profileKey identifies which saved profile a target belongs to: protocol,
// host, port, and user together name an account on a server. StartPath and
// Name can change without it becoming a different profile.
type profileKey struct {
	protocol, host string
	port           int
	user           string
}

func targetKey(t session.Target) profileKey {
	return profileKey{protocol: t.Protocol, host: t.Host, port: t.Port, user: t.User}
}

// profileIndexFor returns the Profile field index (1-based; 0 is "new") of
// the saved profile matching target's key, or 0 if there is none.
func (m Model) profileIndexFor(target session.Target) int {
	key := targetKey(target)
	for i, p := range m.profiles {
		if targetKey(p) == key {
			return i + 1
		}
	}
	return 0
}

// loadConnectProfile fills the form's Protocol/Host/Port/Username/Path from
// the saved profile at the Profile field's current selection. It is a no-op
// when the selection is "(new)".
func (m *Model) loadConnectProfile() {
	if m.connectForm.profile == 0 {
		return
	}
	p := m.profiles[m.connectForm.profile-1]
	m.connectForm.name = p.Name
	m.connectForm.protocol = protocolIndex(p.Protocol)
	m.connectForm.host = p.Host
	m.connectForm.port = ""
	if p.Port != 0 {
		m.connectForm.port = strconv.Itoa(p.Port)
	}
	m.connectForm.username = p.User
	m.connectForm.path = p.StartPath
	m.markConnectFieldsFresh(connectFieldName, connectFieldHost, connectFieldPort, connectFieldUsername, connectFieldPath)
}

// markConnectFieldsFresh flags the given fields as showing a prefilled value
// rather than something the user typed — see connectFormValue.fresh.
func (m *Model) markConnectFieldsFresh(fields ...connectField) {
	for _, f := range fields {
		m.connectForm.fresh[f] = true
	}
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
		profile:  m.profileIndexFor(src),
		name:     src.Name,
		protocol: protocolIndex(src.Protocol),
		host:     src.Host,
		username: src.User,
		path:     src.StartPath,
	}
	if src.Port != 0 {
		m.connectForm.port = strconv.Itoa(src.Port)
	}
	m.markConnectFieldsFresh(connectFieldName, connectFieldHost, connectFieldPort, connectFieldUsername, connectFieldPath)
	m.connectField = connectFieldProfile
	m.connectCursor = 0
	m.overlay = overlayConnect
}

func connectFieldLabel(field connectField) string {
	switch field {
	case connectFieldProfile:
		return "Profile"
	case connectFieldName:
		return "Name"
	case connectFieldProtocol:
		return "Protocol"
	case connectFieldHost:
		return "Host"
	case connectFieldPort:
		return "Port"
	case connectFieldUsername:
		return "Username"
	case connectFieldAuth:
		return "Auth"
	case connectFieldPassword:
		return "Password"
	case connectFieldPath:
		return "Path"
	}
	return ""
}

func (m Model) connectFieldValue(field connectField) string {
	switch field {
	case connectFieldProfile:
		choices := m.connectProfileChoices()
		if m.connectForm.profile < len(choices) {
			return choices[m.connectForm.profile]
		}
		return connectProfileNew
	case connectFieldName:
		return m.connectForm.name
	case connectFieldProtocol:
		return connectProtocols[m.connectForm.protocol]
	case connectFieldHost:
		return m.connectForm.host
	case connectFieldPort:
		return m.connectForm.port
	case connectFieldUsername:
		return m.connectForm.username
	case connectFieldAuth:
		return connectAuthChoices[m.connectForm.auth]
	case connectFieldPassword:
		return m.connectForm.password
	case connectFieldPath:
		return m.connectForm.path
	}
	return ""
}

func (m *Model) setConnectFieldValue(field connectField, value string) {
	switch field {
	case connectFieldName:
		m.connectForm.name = value
	case connectFieldHost:
		m.connectForm.host = value
	case connectFieldPort:
		m.connectForm.port = value
	case connectFieldUsername:
		m.connectForm.username = value
	case connectFieldPassword:
		m.connectForm.password = value
	case connectFieldPath:
		m.connectForm.path = value
	}
}

// connectChoiceField reports whether the given field is a cycled picker rather
// than free text.
func connectChoiceField(field connectField) bool {
	return field == connectFieldProfile || field == connectFieldProtocol || field == connectFieldAuth
}

// connectFieldDisplay returns the value shown for field, with a caret inserted
// when it is the focused free-text field. Password is masked either way, since
// it is shown even while unfocused whenever the auth mode calls for one.
func (m Model) connectFieldDisplay(field connectField) string {
	value := m.connectFieldValue(field)
	if field == connectFieldPassword {
		value = strings.Repeat("•", len([]rune(value)))
	}
	if field == m.connectField && !connectChoiceField(field) {
		runes := []rune(value)
		cur := min(max(m.connectCursor, 0), len(runes))
		runes = append(runes[:cur], append([]rune{'|'}, runes[cur:]...)...)
		return string(runes)
	}
	return value
}

// moveConnectField steps to the next (or previous) visible field, skipping
// over Auth/Password when the current auth mode hides them.
func (m *Model) moveConnectField(delta int) {
	n := int(connectFieldCount)
	next := m.connectField
	for i := 0; i < n; i++ {
		next = connectField((int(next) + delta + n) % n)
		if m.connectFieldVisible(next) {
			break
		}
	}
	m.connectField = next
	m.connectCursor = len([]rune(m.connectFieldValue(m.connectField)))
}

func (m *Model) moveConnectCursor(delta int) {
	runes := []rune(m.connectFieldValue(m.connectField))
	m.connectCursor = min(max(m.connectCursor+delta, 0), len(runes))
}

func (m *Model) cycleConnectChoice(delta int) {
	switch m.connectField {
	case connectFieldProfile:
		n := len(m.profiles) + 1
		m.connectForm.profile = (m.connectForm.profile + delta + n) % n
		m.loadConnectProfile()
	case connectFieldProtocol:
		n := len(connectProtocols)
		m.connectForm.protocol = (m.connectForm.protocol + delta + n) % n
	case connectFieldAuth:
		n := len(connectAuthChoices)
		m.connectForm.auth = (m.connectForm.auth + delta + n) % n
	}
}

func (m *Model) editConnectField(s string) {
	if m.connectForm.fresh[m.connectField] {
		m.setConnectFieldValue(m.connectField, "")
		m.connectCursor = 0
	}
	m.connectForm.fresh[m.connectField] = false
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
	m.connectForm.fresh[m.connectField] = false
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
	m.connectForm.fresh[m.connectField] = false
	runes := []rune(m.connectFieldValue(m.connectField))
	cur := min(max(m.connectCursor, 0), len(runes))
	if cur >= len(runes) {
		return
	}
	runes = append(runes[:cur], runes[cur+1:]...)
	m.setConnectFieldValue(m.connectField, string(runes))
}

// targetFromForm validates the form's Protocol/Host/Port/Username/Path fields
// and builds the target they describe. On a validation failure it sets the
// form's error and returns ok=false.
func (m *Model) targetFromForm() (session.Target, bool) {
	host := strings.TrimSpace(m.connectForm.host)
	if host == "" {
		m.setError("host is required")
		return session.Target{}, false
	}
	port := 0
	if p := strings.TrimSpace(m.connectForm.port); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			m.setError("port must be a number 1-65535")
			return session.Target{}, false
		}
		port = n
	}
	return session.Target{
		Name:      strings.TrimSpace(m.connectForm.name),
		Protocol:  connectProtocols[m.connectForm.protocol],
		Host:      host,
		Port:      port,
		User:      strings.TrimSpace(m.connectForm.username),
		StartPath: strings.TrimSpace(m.connectForm.path),
	}, true
}

// connectFromForm validates the form and dials it. Validation failures leave
// the overlay open and show the reason; success closes it and connects.
func (m *Model) connectFromForm() tea.Cmd {
	target, ok := m.targetFromForm()
	if !ok {
		return nil
	}
	creds := m.credentialsFromForm()
	if creds.PasswordOnly && creds.Password == "" {
		m.setError("password is required for password auth")
		return nil
	}
	m.overlay = overlayNone
	return m.connect(target, creds)
}

// upsertProfile saves target as a profile, replacing an existing one with the
// same targetKey (same protocol/host/port/user) or appending a new one. It
// returns the profile's index. A blank target.Name is filled in with the
// auto-generated label, so a profile always has something to display even
// when the user never typed into the Name field.
func (m *Model) upsertProfile(target session.Target) int {
	if target.Name == "" {
		target.Name = target.Label()
	}
	key := targetKey(target)
	for i, p := range m.profiles {
		if targetKey(p) == key {
			m.profiles[i] = target
			return i
		}
	}
	m.profiles = append(m.profiles, target)
	return len(m.profiles) - 1
}

// saveConnectProfile validates the form and saves it as a profile, updating
// the Profile field to point at it. Validation failures behave like connect's.
func (m *Model) saveConnectProfile() {
	target, ok := m.targetFromForm()
	if !ok {
		return
	}
	idx := m.upsertProfile(target)
	m.connectForm.profile = idx + 1
	m.setStatus("saved profile " + target.Label())
	m.persist()
}

// deleteConnectProfile removes the profile the Profile field currently points
// at. It is a no-op when the selection is "(new)".
func (m *Model) deleteConnectProfile() {
	if m.connectForm.profile == 0 {
		return
	}
	idx := m.connectForm.profile - 1
	name := m.profiles[idx].Label()
	m.profiles = append(m.profiles[:idx], m.profiles[idx+1:]...)
	m.connectForm.profile = 0
	m.setStatus("deleted profile " + name)
	m.persist()
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
	case "ctrl+s":
		m.saveConnectProfile()
	case "ctrl+x":
		m.deleteConnectProfile()
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
			m.connectForm.fresh[m.connectField] = false
		}
	default:
		if len(msg.Runes) > 0 && !connectChoiceField(m.connectField) {
			m.editConnectField(string(msg.Runes))
		}
	}
	return nil
}
