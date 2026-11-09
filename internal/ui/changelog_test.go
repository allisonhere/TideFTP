package ui

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

const sampleChangelog = `# Changelog

Preamble that should not be shown.

## Unreleased

## v1.2.0

### Fixed

- **Bold** fix with ` + "`code`" + `
  that wraps onto a second line.

### Added

- A new thing.
`

func TestParseChangelog(t *testing.T) {
	got := parseChangelog(sampleChangelog)
	want := []changelogBlock{
		{changelogRelease, "v1.2.0"},
		{changelogGroup, "Fixed"},
		{changelogItem, "Bold fix with code that wraps onto a second line."},
		{changelogGroup, "Added"},
		{changelogItem, "A new thing."},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("parseChangelog = %+v\nwant %+v", got, want)
	}
}

func withChangelog(t *testing.T, src string) {
	t.Helper()
	prev := changelogSource
	SetChangelog(src)
	t.Cleanup(func() { SetChangelog(prev) })
}

func TestSettingsChangelogRowOpensAndReturnsToSettings(t *testing.T) {
	withChangelog(t, sampleChangelog)
	model := loadedModel(t, newScriptedEngine())
	model.overlay = overlaySettings
	model.settingsCursor = slices.Index(model.settingsVisibleFields(), settingsFieldChangelog)
	if model.settingsCursor < 0 {
		t.Fatal("no Changelog row in settings")
	}

	model = settle(t, model, model.activateSettingsField())
	if model.overlay != overlayChangelog {
		t.Fatalf("enter on Changelog left overlay=%v, want the changelog", model.overlay)
	}
	view := model.View()
	for _, want := range []string{"changelog", "v1.2.0", "A new thing."} {
		if !strings.Contains(view, want) {
			t.Errorf("changelog view is missing %q", want)
		}
	}
	if strings.Contains(view, "Preamble") {
		t.Error("changelog view shows the preamble")
	}

	model = press(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.overlay != overlaySettings {
		t.Fatalf("esc from the changelog left overlay=%v, want Settings", model.overlay)
	}
}

func TestChangelogScrollIsClamped(t *testing.T) {
	var b strings.Builder
	b.WriteString("## v9.9.9\n\n### Added\n\n")
	for range 100 {
		b.WriteString("- entry\n")
	}
	withChangelog(t, b.String())
	model := loadedModel(t, newScriptedEngine())
	model.openChangelog()

	model = press(t, model, runes("G"))
	maxOffset := model.changelogTotalRows() - model.changelogVisibleRows()
	if maxOffset <= 0 || model.changelogOffset != maxOffset {
		t.Fatalf("G scrolled to %d, want the last page at %d", model.changelogOffset, maxOffset)
	}
	model = press(t, model, runes("g"))
	model = press(t, model, runes("k"))
	if model.changelogOffset != 0 {
		t.Fatalf("scrolling above the top left offset %d", model.changelogOffset)
	}
	model = press(t, model, runes("q"))
	if model.overlay != overlayNone {
		t.Fatalf("q from a changelog opened outside Settings left overlay=%v", model.overlay)
	}
}

func TestChangelogWithoutBundledText(t *testing.T) {
	withChangelog(t, "")
	model := loadedModel(t, newScriptedEngine())
	model.openChangelog()
	if !strings.Contains(model.View(), "No changelog is bundled") {
		t.Fatal("empty changelog did not say so")
	}
}
