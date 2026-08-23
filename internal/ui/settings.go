package ui

import (
	"fmt"
	"strconv"

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
	settingsFieldCount
)

// settingsToggleChoices is the cycle order for every on/off row (Shadow,
// Icons). Density has its own two values, since "off"/"on" would not read
// as compact/comfortable.
var settingsToggleChoices = []string{"off", "on"}

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
	}
	return ""
}

// moveSettingsCursor steps to the next (or previous) row, wrapping.
func (m *Model) moveSettingsCursor(delta int) {
	n := int(settingsFieldCount)
	m.settingsCursor = ((m.settingsCursor+delta)%n + n) % n
}

// cycleSettingsField changes the value of the row under the cursor.
// Density/Shadow/Icons are two-valued, so direction only matters for
// picking which of the two to land on when it's a real toggle (Shadow,
// Icons just flip); Max Parallel is the one row where direction actually
// counts, reusing adjustMaxParallel's own clamp and persistence. Theme
// does not cycle in place — there can be many themes, so this opens the
// same dedicated picker `t` does, landing back at overlayNone (not this
// overlay) once it closes, exactly like pressing `t` directly would.
func (m *Model) cycleSettingsField(direction int) tea.Cmd {
	field := settingsField(m.settingsCursor)
	switch field {
	case settingsFieldTheme:
		m.overlay = overlayTheme
		m.themePicker.Open(m.theme.Name)
		return nil
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
	}
	m.setStatus(fmt.Sprintf("%s: %s", settingsFieldLabel(field), m.settingsFieldValue(field)))
	return m.persist()
}

// handleSettingsKey routes keys while the settings overlay is open:
// up/down move the row cursor, h/l/left/right/enter change the row under
// it — the same shape handleConnectKey uses for the connect form's picker
// fields, scaled down since every settings row is a fixed, always-visible
// cycle rather than a mix of free text and conditionally shown fields.
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
	case "l", "right", "enter":
		return m.cycleSettingsField(1)
	}
	return nil
}
