package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/config"
	"tideftp/internal/fakefs"
	"tideftp/internal/localfs"
	"tideftp/internal/session"
)

// bookmarksModel is a connected model whose current target is also a *saved*
// profile, so the remote pane has somewhere to keep bookmarks. connectModel
// alone leaves m.profiles empty, which is the unsaved-server case that
// TestBookmarkRefusesOnUnsavedServer covers instead.
func bookmarksModel(t *testing.T) Model {
	t.Helper()
	model := loadedModel(t, newScriptedEngine())
	model.profiles = []session.Target{testTarget}
	return model
}

func TestBookmarkAddsAndPersistsTheLocalDirectory(t *testing.T) {
	var saved []config.Config
	save := func(c config.Config) error { saved = append(saved, c); return nil }

	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, nil, config.Default(), save, nil, "")
	model.width, model.height = 120, 36
	model = settle(t, model, listCmd(model.localFS, paneLocal, model.local.requestToken, model.local.path, model.local.showHidden, listingNavigate))

	want := model.local.path
	model = press(t, model, runes("B"))

	if len(model.localBookmarks) != 1 || model.localBookmarks[0] != want {
		t.Fatalf("localBookmarks = %v, want [%s]", model.localBookmarks, want)
	}
	if len(saved) != 1 {
		t.Fatalf("bookmarking saved %d times, want 1", len(saved))
	}
	if len(saved[0].LocalBookmarks) != 1 || saved[0].LocalBookmarks[0] != want {
		t.Fatalf("saved LocalBookmarks = %v, want [%s]", saved[0].LocalBookmarks, want)
	}
}

// B is the whole add/remove story for the directory you are standing in, so a
// second press must undo the first rather than add a duplicate.
func TestBookmarkIsAToggle(t *testing.T) {
	model := bookmarksModel(t)

	model = press(t, model, runes("B"))
	if len(model.localBookmarks) != 1 {
		t.Fatalf("after one B, localBookmarks = %v, want one entry", model.localBookmarks)
	}

	model = press(t, model, runes("B"))
	if len(model.localBookmarks) != 0 {
		t.Fatalf("after two B, localBookmarks = %v, want empty", model.localBookmarks)
	}
	if !strings.Contains(model.status, "removed bookmark") {
		t.Fatalf("status = %q, want it to report the removal", model.status)
	}
}

// A connection with no saved profile has nowhere to put a remote bookmark.
// It must say so rather than inventing a profile the user never asked to save.
func TestBookmarkRefusesOnUnsavedServer(t *testing.T) {
	model := loadedModel(t, newScriptedEngine()) // no profiles
	model.focus = focusRemote

	model = press(t, model, runes("B"))

	if !model.statusErr || !strings.Contains(model.status, "save this server") {
		t.Fatalf("status = %q (err=%v), want the save-first hint", model.status, model.statusErr)
	}
	if len(model.profiles) != 0 {
		t.Fatalf("refusing to bookmark created a profile: %+v", model.profiles)
	}
	if len(model.localBookmarks) != 0 {
		t.Fatalf("a remote bookmark leaked into the local list: %v", model.localBookmarks)
	}
}

// Remote bookmarks belong to the server they were taken on — /var/www means
// something else on another host.
func TestRemoteBookmarksAreScopedToTheProfile(t *testing.T) {
	model := bookmarksModel(t)
	other := session.Target{Name: "other", Protocol: "sftp", Host: "other.local", User: "allie"}
	model.profiles = append(model.profiles, other)

	model.focus = focusRemote
	model = press(t, model, runes("B"))

	if len(model.profiles[0].Bookmarks) != 1 {
		t.Fatalf("connected profile got %v, want one bookmark", model.profiles[0].Bookmarks)
	}
	if len(model.profiles[1].Bookmarks) != 0 {
		t.Fatalf("the other profile got %v, want none", model.profiles[1].Bookmarks)
	}
	if len(model.localBookmarks) != 0 {
		t.Fatalf("a remote bookmark leaked into the local list: %v", model.localBookmarks)
	}
}

// The local list is not tied to any server, so it must be reachable whichever
// profile is connected.
func TestLocalBookmarksAreSharedAcrossProfiles(t *testing.T) {
	model := bookmarksModel(t)
	model = press(t, model, runes("B")) // local pane has focus by default

	model.target = session.Target{Name: "other", Protocol: "sftp", Host: "other.local", User: "allie"}
	paths, writable := model.bookmarksFor(paneLocal)
	if !writable || len(paths) != 1 {
		t.Fatalf("after switching target, local bookmarks = %v (writable=%v), want the one entry", paths, writable)
	}
}

func TestBookmarkPickerJumpsToTheDirectory(t *testing.T) {
	model := bookmarksModel(t)
	start := model.local.path
	model.localBookmarks = []string{"/"}

	model = press(t, model, runes("b"))
	if model.overlay != overlayBookmarks {
		t.Fatalf("b left overlay=%v, want the bookmark picker", model.overlay)
	}

	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.overlay != overlayNone {
		t.Fatalf("enter left overlay=%v, want it closed", model.overlay)
	}
	if model.local.path == start {
		t.Fatalf("local pane stayed at %s, want it moved to /", start)
	}
}

// An empty list still opens: the overlay is where the user finds out B is
// what fills it, so refusing would be a dead end.
func TestBookmarkPickerOpensWhenEmpty(t *testing.T) {
	model := bookmarksModel(t)

	model = press(t, model, runes("b"))

	if model.overlay != overlayBookmarks {
		t.Fatalf("b on an empty list left overlay=%v, want it open", model.overlay)
	}
}

// The picker acts on the pane it was opened from, captured at open time.
func TestBookmarkPickerBindsToTheOpeningPane(t *testing.T) {
	model := bookmarksModel(t)
	model.focus = focusRemote

	model = press(t, model, runes("b"))

	if model.bookmarkPane != paneRemote {
		t.Fatalf("bookmarkPane = %v, want paneRemote", model.bookmarkPane)
	}
}

// Removing from the picker mirrors the server list's two-press confirmation.
func TestBookmarkDeleteNeedsTwoPresses(t *testing.T) {
	model := bookmarksModel(t)
	model.localBookmarks = []string{"/one", "/two"}
	model = press(t, model, runes("b"))

	model = press(t, model, runes("d"))
	if len(model.localBookmarks) != 2 {
		t.Fatalf("one press removed a bookmark: %v", model.localBookmarks)
	}
	if !strings.Contains(model.status, "press d again") {
		t.Fatalf("status = %q, want it to ask for a second press", model.status)
	}

	model = press(t, model, runes("d"))
	if len(model.localBookmarks) != 1 || model.localBookmarks[0] != "/two" {
		t.Fatalf("second press did not remove the armed row: %v", model.localBookmarks)
	}
}

// The arming is per-row: moving the cursor must re-arm, not remove.
func TestBookmarkDeleteArmingDoesNotFollowTheCursor(t *testing.T) {
	model := bookmarksModel(t)
	model.localBookmarks = []string{"/one", "/two"}
	model = press(t, model, runes("b"))

	model = press(t, model, runes("d")) // arm /one
	model = press(t, model, runes("j")) // move to /two
	model = press(t, model, runes("d")) // must re-arm, not remove

	if len(model.localBookmarks) != 2 {
		t.Fatalf("moving the cursor let a press remove a bookmark: %v", model.localBookmarks)
	}
	if !strings.Contains(model.status, "/two") {
		t.Fatalf("status = %q, want the newly highlighted row armed", model.status)
	}

	model = press(t, model, runes("d"))
	if len(model.localBookmarks) != 1 || model.localBookmarks[0] != "/one" {
		t.Fatalf("removed the wrong bookmark: %v", model.localBookmarks)
	}
}

func TestBookmarkDeleteArmingExpires(t *testing.T) {
	model := bookmarksModel(t)
	model.localBookmarks = []string{"/one"}
	model = press(t, model, runes("b"))
	model = press(t, model, runes("d"))

	model.bookmarkDeleteExpiry = time.Now().Add(-time.Second)

	model = press(t, model, runes("d"))
	if len(model.localBookmarks) != 1 {
		t.Fatalf("a lapsed arming still removed: %v", model.localBookmarks)
	}
	if !strings.Contains(model.status, "press d again") {
		t.Fatalf("status = %q, want it to re-arm", model.status)
	}
}

// snapshotConfig rebuilds the whole config from model fields, so a field with
// nothing behind it is silently zeroed on the next save. localBookmarks is
// held whole for exactly that reason; this is the test that proves it.
func TestLocalBookmarksSurviveAnUnrelatedPersist(t *testing.T) {
	var saved []config.Config
	save := func(c config.Config) error { saved = append(saved, c); return nil }

	cfg := config.Default()
	cfg.LocalBookmarks = []string{"/home/allie/Projects"}
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, nil, cfg, save, nil, "")
	model.width, model.height = 120, 36

	model = press(t, model, runes("i")) // toggle icons — nothing to do with bookmarks

	if len(saved) != 1 {
		t.Fatalf("icon toggle saved %d times, want 1", len(saved))
	}
	if len(saved[0].LocalBookmarks) != 1 || saved[0].LocalBookmarks[0] != "/home/allie/Projects" {
		t.Fatalf("an unrelated save zeroed the bookmarks: %v", saved[0].LocalBookmarks)
	}
}

// The bookmarks a profile carries must survive the round trip through the
// config shape in both directions.
func TestProfileBookmarksRoundTripThroughConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Profiles = []config.Profile{{
		Name: "prod", Protocol: "sftp", Host: "web1.example.com", Port: 22, User: "deploy",
		Bookmarks: []string{"/var/www", "/var/log/nginx"},
	}}

	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, nil, cfg, nil, nil, "")

	if got := model.profiles[0].Bookmarks; len(got) != 2 || got[0] != "/var/www" {
		t.Fatalf("loaded profile bookmarks = %v, want the two paths", got)
	}
	if got := model.snapshotConfig().Profiles[0].Bookmarks; len(got) != 2 || got[1] != "/var/log/nginx" {
		t.Fatalf("snapshotted profile bookmarks = %v, want the two paths", got)
	}
}

// The conversion copies rather than shares, so a bookmark added to the model
// cannot write through into a config snapshot taken earlier.
func TestProfileBookmarksAreNotAliased(t *testing.T) {
	cfg := config.Default()
	cfg.Profiles = []config.Profile{{
		Name: "prod", Protocol: "sftp", Host: "web1.example.com", Port: 22, User: "deploy",
		Bookmarks: []string{"/var/www"},
	}}

	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, nil, cfg, nil, nil, "")

	model.profiles[0].Bookmarks[0] = "/changed"

	if cfg.Profiles[0].Bookmarks[0] != "/var/www" {
		t.Fatalf("the model wrote through into the source config: %v", cfg.Profiles[0].Bookmarks)
	}
}

// B inside the picker adds to the list being shown, so building one up is a
// repeated key rather than a close-navigate-reopen cycle. It must act on the
// pane the picker was opened for, not on whatever holds focus.
func TestBookmarkPickerAddsCurrentDirectory(t *testing.T) {
	model := bookmarksModel(t)
	want := model.local.path

	model = press(t, model, runes("b"))
	model = press(t, model, runes("B"))

	if model.overlay != overlayBookmarks {
		t.Fatalf("B closed the picker: overlay=%v", model.overlay)
	}
	if len(model.localBookmarks) != 1 || model.localBookmarks[0] != want {
		t.Fatalf("localBookmarks = %v, want [%s]", model.localBookmarks, want)
	}
}

// b toggles the picker shut, which is why the add hint must not read as `b`.
func TestBookmarkPickerClosesOnB(t *testing.T) {
	model := bookmarksModel(t)
	model = press(t, model, runes("b"))

	model = press(t, model, runes("b"))

	if model.overlay != overlayNone {
		t.Fatalf("b left overlay=%v, want it closed", model.overlay)
	}
	if len(model.localBookmarks) != 0 {
		t.Fatalf("closing the picker added a bookmark: %v", model.localBookmarks)
	}
}

// The connect form has no bookmark field, so a target built from it carries
// none. Editing a saved server must not throw its bookmarks away — upsert
// replaces the whole profile, so the list has to be carried across by hand.
func TestEditingASavedServerKeepsItsBookmarks(t *testing.T) {
	model := bookmarksModel(t)
	model.profiles[0].Bookmarks = []string{"/var/www"}

	edited := testTarget
	edited.Name = "renamed"
	model.upsertProfile(edited)

	if len(model.profiles) != 1 {
		t.Fatalf("upsert of the same key made %d profiles, want 1", len(model.profiles))
	}
	if model.profiles[0].Name != "renamed" {
		t.Fatalf("the edit did not apply: name = %q", model.profiles[0].Name)
	}
	if got := model.profiles[0].Bookmarks; len(got) != 1 || got[0] != "/var/www" {
		t.Fatalf("editing the profile lost its bookmarks: %v", got)
	}
}

// A key-changing edit is a different server, so it correctly starts empty.
func TestUpsertingADifferentServerDoesNotInheritBookmarks(t *testing.T) {
	model := bookmarksModel(t)
	model.profiles[0].Bookmarks = []string{"/var/www"}

	other := testTarget
	other.Host = "elsewhere.local"
	model.upsertProfile(other)

	if len(model.profiles) != 2 {
		t.Fatalf("a new key made %d profiles, want 2", len(model.profiles))
	}
	if len(model.profiles[1].Bookmarks) != 0 {
		t.Fatalf("a different server inherited bookmarks: %v", model.profiles[1].Bookmarks)
	}
}

// The filter's key handler runs before the top-level bindings, so typing a
// `b` into a filter must narrow the listing rather than open the picker.
func TestBookmarkKeysDoNotFireWhileFiltering(t *testing.T) {
	model := bookmarksModel(t)
	model = press(t, model, runes("/"))

	model = press(t, model, runes("b"))

	if model.overlay != overlayNone {
		t.Fatalf("b while filtering opened overlay=%v", model.overlay)
	}
	if model.local.filter != "b" {
		t.Fatalf("local filter = %q, want it to have taken the b", model.local.filter)
	}
}

// Bookmarking while disconnected is a different problem from bookmarking an
// unsaved server, and pointing the user at the wrong one wastes their time.
func TestBookmarkReportsNotConnectedRatherThanUnsaved(t *testing.T) {
	model := bookmarksModel(t)
	model.conn = nil
	model.remoteFS = nil
	model.state = connDisconnected
	model.target = session.Target{}
	model.focus = focusRemote

	model = press(t, model, runes("B"))

	if !strings.Contains(model.status, "not connected") {
		t.Fatalf("status = %q, want it to report the disconnection", model.status)
	}
}

// goldenModel, not bookmarksModel: it pins the pane listings and timestamps,
// so the render does not depend on whatever the real internal/ui directory
// happens to contain when the test runs.
func TestGoldenBookmarksOverlay(t *testing.T) {
	model := goldenModel(t)
	model.localBookmarks = []string{"/home/allie/Projects", "/etc/nginx"}
	model = press(t, model, runes("b"))

	assertGolden(t, "bookmarks_overlay", model.View())
}
