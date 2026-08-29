package ui

import (
	"fmt"
	"os/exec"
	"slices"
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
	settingsFieldEditor
	settingsFieldCount
)

// settingsToggleChoices is the cycle order for every on/off row (Shadow,
// Icons). Density has its own two values, since "off"/"on" would not read
// as compact/comfortable.
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
	}
	return ""
}

// moveSettingsCursor steps to the next (or previous) row, wrapping.
func (m *Model) moveSettingsCursor(delta int) {
	n := int(settingsFieldCount)
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
	field := settingsField(m.settingsCursor)
	switch field {
	case settingsFieldTheme:
		themes := appThemes()
		next := ((themeIndex(m.theme.Name, themes)+direction)%len(themes) + len(themes)) % len(themes)
		m.theme = themes[next]
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
	}
	m.setStatus(fmt.Sprintf("%s: %s", settingsFieldLabel(field), m.settingsFieldValue(field)))
	return m.persist()
}

// activateSettingsField is what enter does on the row under the cursor.
// Every row but Theme just cycles forward, the same as l/right; Theme
// instead opens the full picker to browse/search/preview, since h/l
// already cover quick live cycling one at a time without leaving Settings.
func (m *Model) activateSettingsField() tea.Cmd {
	if settingsField(m.settingsCursor) == settingsFieldTheme {
		m.overlay = overlayTheme
		m.themePicker.Open(m.theme.Name)
		return nil
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
