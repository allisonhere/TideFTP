package ui

import (
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/allisonhere/tideui"
)

// settingsField names one row in the settings overlay, in display order.
type settingsField int

const (
	settingsFieldTheme settingsField = iota
	settingsFieldDensity
	settingsFieldShadow
	settingsFieldIcons
	settingsFieldMaxParallel
	settingsFieldEditor
	settingsFieldVerify
	settingsFieldReconnect
	// The update rows sit last so the settings people change often stay at
	// the top. UpdateStatus is an action row whose meaning depends on the
	// update state (check / install / restart), and UpdateIgnore only exists
	// while there is something to ignore — see settingsFieldVisible.
	settingsFieldUpdateCheck
	settingsFieldUpdateStatus
	settingsFieldUpdateIgnore
	settingsFieldCount
)

// settingsToggleChoices is the cycle order for every on/off row (Shadow,
// Icons, Verify, Reconnect). Density has its own two values, since "off"/"on" would
// not read as compact/comfortable.
var settingsToggleChoices = []string{"off", "on"}

// editorCandidates are the editors the Editor row cycles through, if found on
// PATH. probe is the binary to look for; command is what actually runs, so a
// GUI editor can carry the flag that makes it block.
var editorCandidates = []struct{ probe, command string }{
	{"nano", "nano"}, {"vim", "vim"}, {"nvim", "nvim"}, {"vi", "vi"},
	{"micro", "micro"}, {"hx", "hx"}, {"helix", "helix"}, {"emacs", "emacs"},
	{"kak", "kak"}, {"code", "code -w"}, {"subl", "subl -w"}, {"zed", "zed -w"},
}

// editorChoices is the Editor row's cycle order: "auto" plus every candidate
// present on PATH.
func editorChoices() []string {
	choices := []string{"auto"}
	for _, c := range editorCandidates {
		if _, err := exec.LookPath(c.probe); err == nil {
			choices = append(choices, c.command)
		}
	}
	return choices
}

func settingsFieldLabel(field settingsField) string {
	switch field {
	case settingsFieldTheme:
		return "Theme"
	case settingsFieldDensity:
		return "Density"
	case settingsFieldShadow:
		return "Shadow"
	case settingsFieldIcons:
		return "Icons"
	case settingsFieldMaxParallel:
		return "Max Parallel"
	case settingsFieldEditor:
		return "Editor"
	case settingsFieldVerify:
		return "Verify"
	case settingsFieldReconnect:
		return "Reconnect"
	case settingsFieldUpdateCheck:
		return "Check for updates"
	case settingsFieldUpdateStatus:
		return "Updates"
	case settingsFieldUpdateIgnore:
		return "Ignore this version"
	}
	return ""
}

func (m Model) settingsFieldValue(field settingsField) string {
	switch field {
	case settingsFieldTheme:
		return m.theme.Name
	case settingsFieldDensity:
		return string(m.density)
	case settingsFieldShadow:
		return settingsToggleChoices[boolToIndex(m.shadow)]
	case settingsFieldIcons:
		return settingsToggleChoices[boolToIndex(m.showIcons)]
	case settingsFieldMaxParallel:
		return strconv.Itoa(m.maxParallel)
	case settingsFieldEditor:
		if m.editorSetting == "" {
			if got := detectedEditor(); got != "" {
				return "auto (" + got + ")"
			}
			return "auto (none found)"
		}
		return m.editorSetting
	case settingsFieldVerify:
		return settingsToggleChoices[boolToIndex(m.verifyChecksums)]
	case settingsFieldReconnect:
		return settingsToggleChoices[boolToIndex(m.autoReconnect)]
	case settingsFieldUpdateCheck:
		return settingsToggleChoices[boolToIndex(m.updates.CheckOnStartup)]
	case settingsFieldUpdateStatus:
		return m.settingsUpdateStatus()
	case settingsFieldUpdateIgnore:
		return "enter"
	}
	return ""
}

// settingsUpdateStatus is the Updates row's right-hand text. It doubles as
// the row's meaning: what it says is what enter will act on.
func (m Model) settingsUpdateStatus() string {
	switch m.update.state {
	case updateChecking:
		return "checking…"
	case updateAvailable:
		if m.update.latest.Version == m.updates.DismissedVersion {
			return m.update.latest.Version + " available (ignored)"
		}
		return m.update.latest.Version + " available — enter"
	case updateDownloading, updateInstalling:
		return fmt.Sprintf("installing… %d%%", m.update.percent)
	case updateInstalled:
		return "restart to finish"
	case updateNeedsElevation:
		return "manual install needed — enter"
	case updateFailed:
		return "check failed — enter to retry"
	}
	if !m.update.checked {
		return m.runningVersionLabel() + " — enter to check"
	}
	if m.updates.LastCheckedUnix == 0 {
		return "up to date"
	}
	return "up to date · checked " + relativeSince(time.Unix(m.updates.LastCheckedUnix, 0))
}

// runningVersionLabel names the running build for display.
func (m Model) runningVersionLabel() string {
	if m.version == "" {
		return "dev build"
	}
	return m.version
}

// settingsFieldVisible reports whether a row is shown at all. Every row is
// always visible except Ignore, which only means something once a check has
// actually found a version to ignore.
func (m Model) settingsFieldVisible(field settingsField) bool {
	if field == settingsFieldUpdateIgnore {
		return m.updateAvailable()
	}
	return true
}

// settingsVisibleFields is the rows the overlay draws, in display order. The
// settings cursor indexes into this, not into the enum, so a row appearing or
// vanishing cannot leave the cursor pointing at the wrong setting.
func (m Model) settingsVisibleFields() []settingsField {
	fields := make([]settingsField, 0, int(settingsFieldCount))
	for field := settingsField(0); field < settingsFieldCount; field++ {
		if m.settingsFieldVisible(field) {
			fields = append(fields, field)
		}
	}
	return fields
}

// settingsFieldAt resolves the row cursor to the field under it.
func (m Model) settingsFieldAt(row int) settingsField {
	fields := m.settingsVisibleFields()
	if row < 0 || row >= len(fields) {
		return settingsFieldTheme
	}
	return fields[row]
}

// clampSettingsCursor keeps the cursor inside the visible rows. It matters
// after anything that can remove one — pressing Ignore retires the very row
// the cursor is sitting on.
func (m *Model) clampSettingsCursor() {
	m.settingsCursor = min(max(0, m.settingsCursor), max(0, len(m.settingsVisibleFields())-1))
}

// moveSettingsCursor steps to the next (or previous) row, wrapping.
func (m *Model) moveSettingsCursor(delta int) {
	n := len(m.settingsVisibleFields())
	if n == 0 {
		m.settingsCursor = 0
		return
	}
	m.settingsCursor = ((m.settingsCursor+delta)%n + n) % n
}

// themeIndex finds name's position among themes, defaulting to 0 (an
// unknown name should not happen — m.theme.Name always comes from
// appThemes() itself — but cycling has to land somewhere rather than
// panic if it ever did).
func themeIndex(name string, themes []tideui.Theme) int {
	for i, theme := range themes {
		if theme.Name == name {
			return i
		}
	}
	return 0
}

// cycleSettingsField changes the value of the row under the cursor and
// applies it live, without leaving the settings overlay. Theme steps to
// the next/previous entry in appThemes(), wrapping — the same live-preview
// feel the full picker (opened via enter, or `t` outside Settings) gives
// while browsing, just one theme at a time instead of a scrollable list.
// Density/Shadow/Icons are two-valued, so direction only matters for
// picking which of the two to land on when it's a real toggle (Shadow,
// Icons just flip); Max Parallel is the one row where direction actually
// counts, reusing adjustMaxParallel's own clamp and persistence.
func (m *Model) cycleSettingsField(direction int) tea.Cmd {
	field := m.settingsFieldAt(m.settingsCursor)
	switch field {
	case settingsFieldUpdateStatus, settingsFieldUpdateIgnore:
		// Action rows. h/l must not fire a network check or an install just
		// because the user arrowed across the list.
		return nil
	case settingsFieldUpdateCheck:
		m.updates.CheckOnStartup = !m.updates.CheckOnStartup
		if m.updates.CheckOnStartup {
			m.setStatus("update check on startup: on")
		} else {
			m.setStatus("update check on startup: off")
		}
		return m.persist()
	case settingsFieldTheme:
		themes := appThemes()
		next := ((themeIndex(m.theme.Name, themes)+direction)%len(themes) + len(themes)) % len(themes)
		m.theme = themes[next]
		if m.theme.Name == themeNameMatchOmarchy {
			if t, ok := resolveOmarchyTheme(); ok {
				m.theme = t
			}
		}
	case settingsFieldDensity:
		if m.density == tideui.Compact {
			m.density = tideui.Comfortable
		} else {
			m.density = tideui.Compact
		}
	case settingsFieldShadow:
		m.shadow = !m.shadow
	case settingsFieldIcons:
		m.showIcons = !m.showIcons
	case settingsFieldMaxParallel:
		return m.adjustMaxParallel(direction)
	case settingsFieldEditor:
		choices := editorChoices()
		cur := slices.Index(choices, m.editorSetting)
		if m.editorSetting == "" || cur < 0 {
			cur = 0
		}
		next := ((cur+direction)%len(choices) + len(choices)) % len(choices)
		if next == 0 {
			m.editorSetting = ""
		} else {
			m.editorSetting = choices[next]
		}
	case settingsFieldVerify:
		m.verifyChecksums = !m.verifyChecksums
	case settingsFieldReconnect:
		m.autoReconnect = !m.autoReconnect
		if !m.autoReconnect {
			m.cancelReconnect()
		}
	}
	m.setStatus(fmt.Sprintf("%s: %s", settingsFieldLabel(field), m.settingsFieldValue(field)))
	if field == settingsFieldTheme {
		if m.theme.Name == themeNameMatchOmarchy {
			return tea.Batch(m.persist(), m.startOmarchyWatch())
		}
		m.omarchyWatching = false
	}
	return m.persist()
}

// activateSettingsField is what enter does on the row under the cursor.
// Every row but Theme just cycles forward, the same as l/right; Theme
// instead opens the full picker to browse/search/preview, since h/l
// already cover quick live cycling one at a time without leaving Settings.
func (m *Model) activateSettingsField() tea.Cmd {
	switch m.settingsFieldAt(m.settingsCursor) {
	case settingsFieldTheme:
		m.overlay = overlayTheme
		m.themePicker.Open(m.theme.Name)
		return nil
	case settingsFieldUpdateStatus:
		return m.activateUpdateRow()
	case settingsFieldUpdateIgnore:
		cmd := m.dismissUpdate()
		// The row the cursor was on has just retired itself.
		m.clampSettingsCursor()
		return cmd
	}
	return m.cycleSettingsField(1)
}

// handleSettingsKey routes keys while the settings overlay is open:
// up/down move the row cursor, h/left and l/right cycle the row under it
// live, enter activates it (see activateSettingsField) — the same shape
// handleConnectKey uses for the connect form's picker fields, scaled down
// since every settings row is a fixed, always-visible cycle rather than a
// mix of free text and conditionally shown fields.
func (m *Model) handleSettingsKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q", ",":
		m.overlay = overlayNone
	case "up", "k":
		m.moveSettingsCursor(-1)
	case "down", "j":
		m.moveSettingsCursor(1)
	case "h", "left":
		return m.cycleSettingsField(-1)
	case "l", "right":
		return m.cycleSettingsField(1)
	case "enter":
		return m.activateSettingsField()
	}
	return nil
}
