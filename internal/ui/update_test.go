package ui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/config"
	"tideftp/internal/domain"
	"tideftp/internal/fakefs"
	"tideftp/internal/localfs"
	"tideftp/internal/session"
	"tideftp/internal/update"
)

// availableModel is a model that has just been told a newer release exists,
// without any network involved: the check result is fed in as the message a
// real check would have produced.
func availableModel(t *testing.T, version string) Model {
	t.Helper()
	model := loadedModel(t, newScriptedEngine())
	model.version = "v0.2.1"
	model = settle(t, model, model.applyUpdateChecked(updateCheckedMsg{
		result: update.CheckResult{
			CurrentVersion: "v0.2.1",
			Available:      true,
			Latest: update.ReleaseInfo{
				Version:     version,
				Summary:     "Faster mirrors.",
				DownloadURL: "https://github.com/allisonhere/TideFTP/releases/download/x.tar.gz",
			},
		},
	}))
	return model
}

// The startup check must not fire for a build that could never act on the
// answer — IsNewerVersion refuses to compare an unparseable version, so the
// request would be made purely to throw away. This is also what keeps the
// test suite off the network.
func TestStartupCheckSkipsUnreleasedBuilds(t *testing.T) {
	for name, version := range map[string]string{
		"empty":        "",
		"dev":          "dev",
		"git describe": "v0.2.1-4-gabc123",
	} {
		t.Run(name, func(t *testing.T) {
			model := loadedModel(t, newScriptedEngine())
			model.version = version
			model.updates.CheckOnStartup = true
			if cmd := model.maybeCheckForUpdatesCmd(); cmd != nil {
				t.Fatal("an unreleased build must not check for updates")
			}
		})
	}
}

func TestStartupCheckHonoursTheSetting(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.version = "v0.2.1"

	model.updates.CheckOnStartup = false
	if cmd := model.maybeCheckForUpdatesCmd(); cmd != nil {
		t.Fatal("check ran with the setting off")
	}
	model.updates.CheckOnStartup = true
	if cmd := model.maybeCheckForUpdatesCmd(); cmd == nil {
		t.Fatal("check did not run with the setting on")
	}
}

// snapshotConfig rebuilds config.Config from model fields, so a persisted
// field with nothing behind it in the model is silently zeroed on the next
// save. That would quietly lose the dismissed version and the check setting.
func TestUpdateSettingsSurviveASave(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.updates.CheckOnStartup = false
	model.updates.DismissedVersion = "v9.9.9"
	model.updates.LastCheckedUnix = 1788969039

	got := model.snapshotConfig().Updates

	if got.CheckOnStartup || got.DismissedVersion != "v9.9.9" || got.LastCheckedUnix != 1788969039 {
		t.Fatalf("snapshotConfig dropped the Updates block: %+v", got)
	}
}

func TestTopbarShowsTheUpdateNotice(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	if notice := model.updateNoticeText(); notice != "" {
		t.Fatalf("idle model shows a notice: %q", notice)
	}

	model = availableModel(t, "v0.3.0")
	if !strings.Contains(model.updateNoticeText(), "v0.3.0") {
		t.Fatalf("notice = %q, want it to name the version", model.updateNoticeText())
	}
	if notice := model.updateNoticeText(); !strings.Contains(notice, "↑ UPDATE") || !strings.Contains(notice, "· U") {
		t.Fatalf("notice = %q, want an explicit update alert and shortcut", notice)
	}
	// It must not advertise `i`: that key is toggle-icons everywhere else.
	if strings.Contains(model.updateNoticeText(), "i ignore") {
		t.Fatal("the notice advertises i, which is already bound to icons")
	}
}

func TestUpdateNoticeUsesAmberWarningColors(t *testing.T) {
	if got := updateNoticeStyle.GetBackground(); got != updateNoticeBackground {
		t.Fatalf("update notice background = %v, want orange %v", got, updateNoticeBackground)
	}
	if got := updateNoticeStyle.GetForeground(); got != updateNoticeForeground {
		t.Fatalf("update notice foreground = %v, want %v", got, updateNoticeForeground)
	}
}

// Ignoring suppresses this exact version and no other, so a later release
// still gets through.
func TestIgnoreSuppressesOnlyThatVersion(t *testing.T) {
	model := availableModel(t, "v0.3.0")
	model = settle(t, model, model.dismissUpdate())

	if model.updates.DismissedVersion != "v0.3.0" {
		t.Fatalf("dismissed = %q, want v0.3.0", model.updates.DismissedVersion)
	}
	if model.updateAvailable() || model.updateNoticeText() != "" {
		t.Fatal("the ignored version is still being advertised")
	}

	model = settle(t, model, model.applyUpdateChecked(updateCheckedMsg{
		result: update.CheckResult{Available: true, Latest: update.ReleaseInfo{Version: "v0.4.0"}},
	}))
	if !model.updateAvailable() {
		t.Fatal("a newer release than the ignored one must still surface")
	}
}

// A check that finds nothing clears a stale dismissal, so a version dismissed
// long ago cannot suppress a future release that happens to reuse the string.
func TestNoUpdateClearsAStaleDismissal(t *testing.T) {
	model := availableModel(t, "v0.3.0")
	model = settle(t, model, model.dismissUpdate())
	model = settle(t, model, model.applyUpdateChecked(updateCheckedMsg{
		result: update.CheckResult{Available: false},
	}))
	if model.updates.DismissedVersion != "" {
		t.Fatalf("dismissed = %q, want it cleared", model.updates.DismissedVersion)
	}
}

// A failed startup check stays quiet — the app works fine without knowing,
// and an error banner for a network blip nobody asked about is noise. A
// failed manual check reports, because someone is waiting on it.
func TestCheckFailureIsQuietOnStartupAndLoudWhenAsked(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model = settle(t, model, model.applyUpdateChecked(updateCheckedMsg{err: errors.New("no route to host")}))
	if model.statusErr {
		t.Fatalf("a background check failure raised an error: %q", model.status)
	}

	model = settle(t, model, model.applyUpdateChecked(updateCheckedMsg{err: errors.New("no route to host"), manual: true}))
	if !model.statusErr || !strings.Contains(model.status, "no route to host") {
		t.Fatalf("status = %q, want the failure reported", model.status)
	}
}

// Installing swaps the running binary and the restart drops whatever is in
// flight, so a busy queue gets a warning — but it is a warning, not a
// refusal: a long mirror must not be able to lock the user out of updating.
func TestInstallWarnsOnceWhileTransfersAreRunning(t *testing.T) {
	model := availableModel(t, "v0.3.0")
	model.transfers = []domain.Transfer{{ID: 1, Status: domain.Active}}
	model.overlay = overlayUpdate

	updated, cmd := model.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("the first enter started the install without warning")
	}
	if !model.updateBusyAck || !strings.Contains(model.status, "still running") {
		t.Fatalf("status = %q, want a busy-queue warning", model.status)
	}
	if model.overlay != overlayUpdate {
		t.Fatal("the overlay closed instead of asking again")
	}

	// Second press goes through.
	updated, cmd = model.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("the second enter did not start the install")
	}
	if model.update.state != updateDownloading {
		t.Fatalf("state = %v, want updateDownloading", model.update.state)
	}
}

func TestInstallDoesNotWarnWithAnIdleQueue(t *testing.T) {
	model := availableModel(t, "v0.3.0")
	model.overlay = overlayUpdate

	updated, cmd := model.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("an idle queue should install on the first enter")
	}
	if updated.(Model).update.state != updateDownloading {
		t.Fatal("install did not start")
	}
}

// RequiresManual comes back with a nil error: the download worked and was
// verified, only the install target was not writable. Reporting that as a
// failure would say the update broke when the fix is one printed command.
func TestNotWritableTargetIsNotAFailure(t *testing.T) {
	model := availableModel(t, "v0.3.0")
	model = settle(t, model, model.applyUpdateInstalled(updateInstalledMsg{
		result: update.InstallResult{RequiresManual: true, ManualCommand: "sudo cp ..."},
	}))

	if model.update.state != updateNeedsElevation {
		t.Fatalf("state = %v, want updateNeedsElevation", model.update.state)
	}
	if model.update.manualCommand != "sudo cp ..." {
		t.Fatalf("manual command = %q", model.update.manualCommand)
	}
	if model.restartExec != "" {
		t.Fatal("nothing was installed, so there is nothing to restart into")
	}
}

func TestInstalledRecordsTheRestartTarget(t *testing.T) {
	model := availableModel(t, "v0.3.0")
	installed := filepath.Join(t.TempDir(), "tideftp")
	model = settle(t, model, model.applyUpdateInstalled(updateInstalledMsg{
		result: update.InstallResult{Restartable: true, ExecutablePath: installed, Version: "v0.3.0"},
	}))

	if model.update.state != updateInstalled {
		t.Fatalf("state = %v, want updateInstalled", model.update.state)
	}
	if model.RestartExecPath() != installed {
		t.Fatalf("restart path = %q, want %q", model.RestartExecPath(), installed)
	}

	// Enter from the installed overlay quits, so main can exec the new binary.
	model.overlay = overlayUpdate
	_, cmd := model.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on the installed overlay did not quit")
	}
}

// An install that never happened must leave nothing for main to exec.
func TestNoInstallMeansNoRestart(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	if path := model.RestartExecPath(); path != "" {
		t.Fatalf("restart path = %q, want empty", path)
	}
}

// Keys must be swallowed while the download or install is in flight, so a
// stray enter cannot start a second one or close the overlay mid-write.
func TestUpdateOverlaySwallowsKeysWhileBusy(t *testing.T) {
	for _, state := range []updateState{updateDownloading, updateInstalling} {
		model := availableModel(t, "v0.3.0")
		model.update.state = state
		model.overlay = overlayUpdate

		updated, cmd := model.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd != nil || updated.(Model).overlay != overlayUpdate {
			t.Fatalf("state %v: enter was not swallowed", state)
		}
	}
}

// The Ignore row only exists while there is something to ignore, and pressing
// it retires the very row the cursor is on — which must not leave the cursor
// pointing past the end.
func TestSettingsIgnoreRowIsConditionalAndClampsTheCursor(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	for _, field := range model.settingsVisibleFields() {
		if field == settingsFieldUpdateIgnore {
			t.Fatal("the Ignore row is visible with no update available")
		}
	}

	model = availableModel(t, "v0.3.0")
	visible := model.settingsVisibleFields()
	if visible[len(visible)-1] != settingsFieldUpdateIgnore {
		t.Fatal("the Ignore row did not appear once an update was available")
	}

	// Park on Ignore — the last row — and activate it.
	model.overlay = overlaySettings
	model.settingsCursor = len(visible) - 1
	model = settle(t, model, model.activateSettingsField())

	if model.settingsCursor >= len(model.settingsVisibleFields()) {
		t.Fatalf("cursor %d is past the last of %d rows", model.settingsCursor, len(model.settingsVisibleFields()))
	}
	if model.settingsFieldAt(model.settingsCursor) == settingsFieldUpdateIgnore {
		t.Fatal("the cursor is still on a row that no longer exists")
	}
}

// Arrowing across the settings list must never fire a network check or start
// an install — the action rows only respond to enter.
func TestSettingsActionRowsIgnoreLeftRight(t *testing.T) {
	model := availableModel(t, "v0.3.0")
	model.overlay = overlaySettings
	before := model.status

	for row, field := range model.settingsVisibleFields() {
		if field != settingsFieldUpdateStatus && field != settingsFieldUpdateIgnore {
			continue
		}
		model.settingsCursor = row
		if cmd := model.cycleSettingsField(1); cmd != nil {
			t.Fatalf("%v: h/l issued a command", settingsFieldLabel(field))
		}
		if model.status != before {
			t.Fatalf("%v: h/l changed the status to %q", settingsFieldLabel(field), model.status)
		}
	}
}

func TestSettingsCheckOnStartupTogglesAndPersists(t *testing.T) {
	var saved []config.Config
	save := func(c config.Config) error { saved = append(saved, c); return nil }
	cfg := config.Default()
	if !cfg.Updates.CheckOnStartup {
		t.Fatal("checking on startup should be the default")
	}
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, []session.Target{testTarget}, cfg, save, nil, "")
	model.width, model.height = 120, 36

	model = press(t, model, runes(","))
	for model.settingsFieldAt(model.settingsCursor) != settingsFieldUpdateCheck {
		model = press(t, model, tea.KeyMsg{Type: tea.KeyDown})
	}
	model = press(t, model, tea.KeyMsg{Type: tea.KeyLeft})

	if model.updates.CheckOnStartup {
		t.Fatal("the toggle did not turn the startup check off")
	}
	if len(saved) == 0 || saved[len(saved)-1].Updates.CheckOnStartup {
		t.Fatalf("the change was not persisted: %+v", saved)
	}
}

// A download over a fast link can finish in a few hundred milliseconds. The
// bar must still cross properly rather than flashing to a third and
// vanishing, so a result that arrives early is held until the bar catches up.
func TestFastInstallIsHeldUntilTheBarFinishes(t *testing.T) {
	model := availableModel(t, "v0.3.0")
	model.update.state = updateInstalling
	model.update.startedAt = time.Now() // the install "just" started

	// The real work reports back immediately.
	model = settle(t, model, model.applyUpdateInstalled(updateInstalledMsg{
		result: update.InstallResult{Restartable: true, ExecutablePath: "/tmp/tideftp"},
	}))

	if model.update.state != updateInstalling {
		t.Fatalf("state = %v, want the bar still running", model.update.state)
	}
	if model.update.pending == nil {
		t.Fatal("the early result was not held")
	}
	if model.update.percent >= 100 {
		t.Fatalf("percent = %d so soon; the bar should still be crossing", model.update.percent)
	}

	// Once the floor has passed, the next tick finishes at a full 100%.
	model.update.startedAt = time.Now().Add(-updateMinDuration)
	model = settle(t, model, model.applyUpdateTick())

	if model.update.percent != 100 {
		t.Fatalf("percent = %d at the end, want exactly 100", model.update.percent)
	}
	if model.update.state != updateInstalled {
		t.Fatalf("state = %v, want updateInstalled", model.update.state)
	}
	if model.RestartExecPath() != "/tmp/tideftp" {
		t.Fatal("the held result was not applied")
	}
}

// The other way round: work that outlasts the bar must not let it sit at
// 100% while something is still happening.
func TestSlowInstallHoldsShortOfComplete(t *testing.T) {
	model := availableModel(t, "v0.3.0")
	model.update.state = updateInstalling
	model.update.startedAt = time.Now().Add(-10 * updateMinDuration)

	model = settle(t, model, model.applyUpdateTick())

	if model.update.percent != 99 {
		t.Fatalf("percent = %d while still working, want it held at 99", model.update.percent)
	}
	if model.update.state != updateInstalling {
		t.Fatalf("state = %v, want it still installing", model.update.state)
	}
}

// A failure is shown at once — there is nothing to make look good, and
// holding an error behind an animation is just a delay.
func TestFailedInstallIsNotPaddedOut(t *testing.T) {
	model := availableModel(t, "v0.3.0")
	model.update.state = updateInstalling
	model.update.startedAt = time.Now()

	model = settle(t, model, model.applyUpdateInstalled(updateInstalledMsg{err: errors.New("disk full")}))

	if model.update.state != updateFailed {
		t.Fatalf("state = %v, want updateFailed straight away", model.update.state)
	}
	if model.update.pending != nil {
		t.Fatal("a failure should not be held back")
	}
}

// The bar is driven by elapsed time, so it crosses in updateMinDuration
// whatever the scheduler does with the ticker.
func TestProgressBarTracksElapsedTime(t *testing.T) {
	model := availableModel(t, "v0.3.0")
	model.update.state = updateDownloading

	for _, tc := range []struct {
		elapsed time.Duration
		want    int
	}{
		{0, 0},
		{updateMinDuration / 4, 25},
		{updateMinDuration / 2, 50},
	} {
		model.update.startedAt = time.Now().Add(-tc.elapsed)
		_ = model.applyUpdateTick()
		if diff := model.update.percent - tc.want; diff < -2 || diff > 2 {
			t.Errorf("at %v percent = %d, want about %d", tc.elapsed, model.update.percent, tc.want)
		}
	}
}
