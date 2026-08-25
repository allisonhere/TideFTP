package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
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
	connectFieldRemember
	connectFieldIdentity
	connectFieldKnownHosts
	connectFieldFTPSVerify
	connectFieldFTPSCA
	connectFieldPath
	connectFieldCount
)

// connectProtocols is the protocol picker's cycle order.
var connectProtocols = []string{"sftp", "ftp", "ftps"}

// connectAuthChoices is the Auth picker's cycle order. It only applies to
// SFTP — FTP and FTPS have no other way to authenticate, so they force
// "password" without showing the field at all; see connectAuthMode.
var connectAuthChoices = []string{"agent/key", "password"}

// connectFTPSVerifyChoices is the FTPS Verify picker's cycle order.
var connectFTPSVerifyChoices = []string{"verify", "insecure"}

// connectRememberChoices is the Remember picker's cycle order.
var connectRememberChoices = []string{"no", "yes"}

const connectIdentityBrowserHeight = 10

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
	profile    int
	name       string
	protocol   int
	host       string
	port       string
	username   string
	auth       int
	password   string
	remember   bool
	identity   string
	knownHosts string
	ftpsVerify int
	ftpsCA     string
	path       string
	fresh      [connectFieldCount]bool

	// credToken guards an in-flight credstore lookup (see
	// beginCredentialLookup): a reply whose token no longer matches this one
	// belongs to a profile selection the user has since moved past, and is
	// dropped rather than clobbering newer form state.
	credToken int
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
// form's current state. Auth, Identity, and KnownHosts only matter for
// SFTP; Identity only when the resolved auth mode actually offers a key
// (there is nothing to name a key file for if the connection will
// authenticate with a password); Password only when the resolved auth mode
// uses one; Remember only alongside Password, and only when a credstore was
// actually wired in — offering to remember a password nothing can store it
// would just be confusing; FTPSVerify and FTPSCA only matter for FTPS, and
// FTPSCA only when Verify is not already "insecure" — a CA to trust is moot
// once every certificate is accepted anyway.
func (m Model) connectFieldVisible(field connectField) bool {
	protocol := connectProtocols[m.connectForm.protocol]
	switch field {
	case connectFieldAuth:
		return protocol == "sftp"
	case connectFieldPassword:
		return m.connectAuthMode() == "password"
	case connectFieldRemember:
		return m.connectAuthMode() == "password" && m.creds != nil
	case connectFieldIdentity:
		return protocol == "sftp" && m.connectAuthMode() != "password"
	case connectFieldKnownHosts:
		return protocol == "sftp"
	case connectFieldFTPSVerify:
		return protocol == "ftps"
	case connectFieldFTPSCA:
		return protocol == "ftps" && connectFTPSVerifyChoices[m.connectForm.ftpsVerify] != "insecure"
	default:
		return true
	}
}

// credentialsFromForm builds what Dial needs to authenticate and verify the
// host. It never touches session.Target — none of this is part of what a
// profile can persist. PasswordOnly is set only for SFTP, where choosing
// "password" explicitly means skipping the agent and key files rather than
// merely offering Password as a fallback after them.
func (m Model) credentialsFromForm() session.Credentials {
	protocol := connectProtocols[m.connectForm.protocol]
	var creds session.Credentials
	if m.connectAuthMode() == "password" {
		creds.Password = m.connectForm.password
		creds.PasswordOnly = protocol == "sftp"
	}
	if protocol == "sftp" {
		creds.IdentityFile = strings.TrimSpace(m.connectForm.identity)
		creds.KnownHostsPath = strings.TrimSpace(m.connectForm.knownHosts)
	}
	if protocol == "ftps" {
		creds.FTPSCAFile = strings.TrimSpace(m.connectForm.ftpsCA)
		creds.FTPSInsecure = connectFTPSVerifyChoices[m.connectForm.ftpsVerify] == "insecure"
	}
	return creds
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

// credentialKey is the opaque key a stored password lives under: the same
// identity targetKey uses, not target.Name, so renaming a saved profile
// never orphans its stored password.
func credentialKey(t session.Target) string {
	k := targetKey(t)
	return fmt.Sprintf("%s|%s|%d|%s", k.protocol, k.host, k.port, k.user)
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
// the saved profile at the Profile field's current selection, and looks up
// whatever password is stored for it. It is a no-op when the selection is
// "(new)" — Password/Remember are left as they were, the same as every
// other field when cycling to "(new)".
func (m *Model) loadConnectProfile() tea.Cmd {
	if m.connectForm.profile == 0 {
		return nil
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
	return m.beginCredentialLookup(p)
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
func (m *Model) openConnectForm() tea.Cmd {
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
	m.connectIdentityBrowse = false
	m.overlay = overlayConnect
	return m.beginCredentialLookup(src)
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
	case connectFieldRemember:
		return "Remember"
	case connectFieldIdentity:
		return "Identity"
	case connectFieldKnownHosts:
		return "Known Hosts"
	case connectFieldFTPSVerify:
		return "Verify"
	case connectFieldFTPSCA:
		return "CA File"
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
	case connectFieldRemember:
		return connectRememberChoices[boolToIndex(m.connectForm.remember)]
	case connectFieldIdentity:
		return m.connectForm.identity
	case connectFieldKnownHosts:
		return m.connectForm.knownHosts
	case connectFieldFTPSVerify:
		return connectFTPSVerifyChoices[m.connectForm.ftpsVerify]
	case connectFieldFTPSCA:
		return m.connectForm.ftpsCA
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
	case connectFieldIdentity:
		m.connectForm.identity = value
	case connectFieldKnownHosts:
		m.connectForm.knownHosts = value
	case connectFieldFTPSCA:
		m.connectForm.ftpsCA = value
	case connectFieldPath:
		m.connectForm.path = value
	}
}

// connectChoiceField reports whether the given field is a cycled picker rather
// than free text.
func connectChoiceField(field connectField) bool {
	switch field {
	case connectFieldProfile, connectFieldProtocol, connectFieldAuth, connectFieldRemember, connectFieldFTPSVerify:
		return true
	default:
		return false
	}
}

// boolToIndex is connectRememberChoices' bool-to-cycle-index mapping: false
// is "no" (index 0), true is "yes" (index 1).
func boolToIndex(b bool) int {
	if b {
		return 1
	}
	return 0
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

func (m *Model) cycleConnectChoice(delta int) tea.Cmd {
	switch m.connectField {
	case connectFieldProfile:
		n := len(m.profiles) + 1
		m.connectForm.profile = (m.connectForm.profile + delta + n) % n
		return m.loadConnectProfile()
	case connectFieldProtocol:
		n := len(connectProtocols)
		m.connectForm.protocol = (m.connectForm.protocol + delta + n) % n
	case connectFieldAuth:
		n := len(connectAuthChoices)
		m.connectForm.auth = (m.connectForm.auth + delta + n) % n
	case connectFieldFTPSVerify:
		n := len(connectFTPSVerifyChoices)
		m.connectForm.ftpsVerify = (m.connectForm.ftpsVerify + delta + n) % n
	case connectFieldRemember:
		m.connectForm.remember = !m.connectForm.remember
	}
	return nil
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

// openConnectIdentityBrowser shows a local file browser inside the connect
// overlay so the SFTP Identity field can be filled without leaving the form.
func (m *Model) openConnectIdentityBrowser() {
	if m.connectField != connectFieldIdentity || !m.connectFieldVisible(connectFieldIdentity) {
		return
	}
	m.connectIdentityPane = filePane{
		title:      "Identity",
		path:       m.local.path,
		entries:    append([]domain.Entry(nil), m.local.entries...),
		cursor:     m.local.cursor,
		offset:     m.local.offset,
		showHidden: true,
		selected:   map[string]bool{},
	}
	m.prependPaneParent(&m.connectIdentityPane, m.localFS)
	m.connectIdentityPane.clamp(connectIdentityBrowserHeight - 1)
	m.connectIdentityBrowse = true
	m.setStatus("browse to an identity file")
}

func (m *Model) navigateConnectIdentityParent() tea.Cmd {
	parent := m.localFS.Parent(m.connectIdentityPane.path)
	if parent == m.connectIdentityPane.path {
		m.setStatus("already at filesystem root")
		return nil
	}
	m.setStatus("browsing " + parent)
	return m.navigateTo(paneIdentity, parent)
}

// chooseConnectIdentityEntry handles the highlighted local entry while the
// Identity browser is open. Directories are opened; files are copied into the
// field and return the user to the form.
func (m *Model) chooseConnectIdentityEntry() tea.Cmd {
	if !m.connectIdentityBrowse {
		return nil
	}
	entry, found := m.connectIdentityPane.current()
	if !found {
		m.setError("highlight a local key file first")
		return nil
	}
	if isParentDirEntry(entry) {
		return m.navigateConnectIdentityParent()
	}
	if entry.IsDir() {
		dirPath := m.localFS.Child(m.connectIdentityPane.path, entry.Name)
		m.setStatus("browsing " + dirPath)
		return m.navigateTo(paneIdentity, dirPath)
	}
	path := m.localFS.Child(m.connectIdentityPane.path, entry.Name)
	m.connectForm.identity = path
	m.connectCursor = len([]rune(path))
	m.connectForm.fresh[connectFieldIdentity] = false
	m.connectIdentityBrowse = false
	m.setStatus("identity file " + path)
	return nil
}

func (m *Model) handleConnectIdentityBrowseKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q":
		m.connectIdentityBrowse = false
		m.setStatus("identity browse cancelled")
	case "up", "k":
		m.connectIdentityPane.cursor--
		m.connectIdentityPane.clamp(connectIdentityBrowserHeight - 1)
	case "down", "j":
		m.connectIdentityPane.cursor++
		m.connectIdentityPane.clamp(connectIdentityBrowserHeight - 1)
	case "pgup":
		m.connectIdentityPane.cursor -= 10
		m.connectIdentityPane.clamp(connectIdentityBrowserHeight - 1)
	case "pgdown":
		m.connectIdentityPane.cursor += 10
		m.connectIdentityPane.clamp(connectIdentityBrowserHeight - 1)
	case "home":
		m.connectIdentityPane.cursor = 0
		m.connectIdentityPane.clamp(connectIdentityBrowserHeight - 1)
	case "end":
		m.connectIdentityPane.cursor = len(m.connectIdentityPane.entries) - 1
		m.connectIdentityPane.clamp(connectIdentityBrowserHeight - 1)
	case "backspace", "h":
		return m.navigateConnectIdentityParent()
	case "enter", "ctrl+b", "l":
		return m.chooseConnectIdentityEntry()
	}
	return nil
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
func (m *Model) saveConnectProfile() tea.Cmd {
	target, ok := m.targetFromForm()
	if !ok {
		return nil
	}
	idx := m.upsertProfile(target)
	m.connectForm.profile = idx + 1
	m.setStatus("saved profile " + target.Label())
	return tea.Batch(m.persist(), m.rememberCredentialCmd(target))
}

// deleteConnectProfile removes the profile the Profile field currently points
// at. It is a no-op when the selection is "(new)".
func (m *Model) deleteConnectProfile() tea.Cmd {
	if m.connectForm.profile == 0 {
		return nil
	}
	idx := m.connectForm.profile - 1
	target := m.profiles[idx]
	name := target.Label()
	m.profiles = append(m.profiles[:idx], m.profiles[idx+1:]...)
	m.connectForm.profile = 0
	m.setStatus("deleted profile " + name)
	return tea.Batch(m.persist(), m.forgetCredentialCmd(target))
}

// storedCredentialMsg reports the result of a credstore lookup begun by
// beginCredentialLookup. A reply whose token no longer matches
// connectForm.credToken belongs to a profile selection the user has since
// moved past, and is dropped.
type storedCredentialMsg struct {
	token    int
	password string
	ok       bool
	err      error
}

// beginCredentialLookup asks the credstore for target's password, clearing
// the form's Password/Remember fields synchronously first — so the form
// never shows a stale value while the (possibly slow: dbus, or a Keychain
// prompt) lookup is in flight — and filling them back in once the reply
// lands, via storedCredentialMsg in Update. A nil creds leaves the fields
// cleared and does nothing further.
func (m *Model) beginCredentialLookup(target session.Target) tea.Cmd {
	m.connectForm.password = ""
	m.connectForm.remember = false
	if m.creds == nil {
		return nil
	}
	m.connectForm.credToken++
	token := m.connectForm.credToken
	store, key := m.creds, credentialKey(target)
	return func() tea.Msg {
		password, ok, err := store.Get(key)
		return storedCredentialMsg{token: token, password: password, ok: ok, err: err}
	}
}

// credentialSyncMsg reports the result of storing or forgetting a password,
// begun by rememberCredentialCmd or forgetCredentialCmd.
type credentialSyncMsg struct{ err error }

// rememberCredentialCmd stores or forgets target's password in the
// credstore according to the form's current Remember/Password fields. Value
// receiver, so it snapshots those fields at call time rather than reading
// them again once the returned command actually runs — the same reason
// Model.persist snapshots its config before returning.
func (m Model) rememberCredentialCmd(target session.Target) tea.Cmd {
	if m.creds == nil || !m.connectFieldVisible(connectFieldRemember) {
		return nil
	}
	store, key := m.creds, credentialKey(target)
	remember, password := m.connectForm.remember, m.connectForm.password
	return func() tea.Msg {
		var err error
		if remember && password != "" {
			err = store.Set(key, password)
		} else {
			err = store.Delete(key)
		}
		return credentialSyncMsg{err: err}
	}
}

// forgetCredentialCmd unconditionally deletes target's stored password, for
// deleteConnectProfile: a deleted profile's password must not survive it,
// regardless of what Remember was last set to.
func (m Model) forgetCredentialCmd(target session.Target) tea.Cmd {
	if m.creds == nil {
		return nil
	}
	store, key := m.creds, credentialKey(target)
	return func() tea.Msg {
		return credentialSyncMsg{err: store.Delete(key)}
	}
}

// handleConnectKey routes keys while the connect overlay is open, mirroring
// whatthedock's form editing: arrows/tab move between fields, h/l cycle the
// picker or move the caret in free text, and ctrl/alt+enter connects.
func (m *Model) handleConnectKey(msg tea.KeyMsg) tea.Cmd {
	if m.connectIdentityBrowse {
		return m.handleConnectIdentityBrowseKey(msg)
	}
	switch msg.String() {
	case "esc", "q":
		m.connectIdentityBrowse = false
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
			return m.cycleConnectChoice(-1)
		}
		m.editConnectField("h")
	case "l":
		if connectChoiceField(m.connectField) {
			return m.cycleConnectChoice(1)
		}
		m.editConnectField("l")
	case "left":
		if connectChoiceField(m.connectField) {
			return m.cycleConnectChoice(-1)
		}
		m.moveConnectCursor(-1)
	case "right":
		if connectChoiceField(m.connectField) {
			return m.cycleConnectChoice(1)
		}
		m.moveConnectCursor(1)
	case "enter":
		if connectChoiceField(m.connectField) {
			return m.cycleConnectChoice(1)
		}
		m.moveConnectField(1)
	case "ctrl+enter", "alt+enter":
		return m.connectFromForm()
	case "ctrl+s":
		return m.saveConnectProfile()
	case "ctrl+x":
		return m.deleteConnectProfile()
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
	case "ctrl+b":
		m.openConnectIdentityBrowser()
	default:
		if len(msg.Runes) > 0 && !connectChoiceField(m.connectField) {
			m.editConnectField(string(msg.Runes))
		}
	}
	return nil
}
