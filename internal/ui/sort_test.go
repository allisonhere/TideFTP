package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/config"
	"tideftp/internal/domain"
)

func mixedEntries() []domain.Entry {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rec := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	return []domain.Entry{
		{Name: "beta.txt", Kind: domain.EntryFile, Size: 300, Modified: mid},
		{Name: "alpha.go", Kind: domain.EntryFile, Size: 100, Modified: rec},
		{Name: "zeta", Kind: domain.EntryDir, Modified: old},
		{Name: "gamma.md", Kind: domain.EntryFile, Size: 200, Modified: old},
		{Name: "delta", Kind: domain.EntryDir, Modified: rec},
	}
}

func order(entries []domain.Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestSortEntriesByName(t *testing.T) {
	e := mixedEntries()
	sortEntries(e, sortByName, false)
	eq(t, order(e), []string{"delta", "zeta", "alpha.go", "beta.txt", "gamma.md"})

	sortEntries(e, sortByName, true)
	eq(t, order(e), []string{"zeta", "delta", "gamma.md", "beta.txt", "alpha.go"})
}

func TestSortEntriesBySizeKeepsDirsFirst(t *testing.T) {
	e := mixedEntries()
	sortEntries(e, sortBySize, false)
	// Dirs first (name-tiebroken), then files small→large.
	eq(t, order(e), []string{"delta", "zeta", "alpha.go", "gamma.md", "beta.txt"})

	sortEntries(e, sortBySize, true)
	eq(t, order(e), []string{"zeta", "delta", "beta.txt", "gamma.md", "alpha.go"})
}

func TestSortEntriesByModified(t *testing.T) {
	e := mixedEntries()
	sortEntries(e, sortByModified, false)
	// Dirs first by mtime (zeta old, delta recent), then files oldest→newest.
	eq(t, order(e), []string{"zeta", "delta", "gamma.md", "beta.txt", "alpha.go"})
}

func TestSortEntriesByKindDoesNotForceDirsFirst(t *testing.T) {
	e := mixedEntries()
	sortEntries(e, sortByKind, false)
	// EntryFile (0) sorts before EntryDir (1); within a kind, by name.
	eq(t, order(e), []string{"alpha.go", "beta.txt", "gamma.md", "delta", "zeta"})
}

func TestSortEntriesPinsParent(t *testing.T) {
	e := append([]domain.Entry{parentDirEntry()}, mixedEntries()...)
	sortEntries(e, sortByName, true) // even reversed, ".." stays on top
	if e[0].Name != parentEntryName {
		t.Fatalf("parent row = %q, want it pinned first", e[0].Name)
	}
}

func TestSortKeyRoundTripsThroughConfig(t *testing.T) {
	for _, k := range []sortKey{sortByName, sortBySize, sortByModified, sortByKind} {
		if got := parseSortKey(k.String()); got != k {
			t.Fatalf("parseSortKey(%q) = %v, want %v", k.String(), got, k)
		}
	}
	if parseSortKey("nonsense") != sortByName {
		t.Fatalf("unknown key did not fall back to name")
	}
}

func TestSKeyCyclesSortAndPersists(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusLocal

	saved := make(chan config.Config, 4)
	model.save = func(c config.Config) error { saved <- c; return nil }

	if model.local.sortKey != sortByName {
		t.Fatalf("start sortKey = %v, want name", model.local.sortKey)
	}
	model = press(t, model, runes("s"))
	if model.local.sortKey != sortBySize {
		t.Fatalf("after one 's' sortKey = %v, want size", model.local.sortKey)
	}
	select {
	case c := <-saved:
		if c.Sort.Key != "size" {
			t.Fatalf("persisted sort key = %q, want size", c.Sort.Key)
		}
	default:
		t.Fatalf("'s' did not persist the new sort")
	}
}

func TestShiftSTogglesDirectionPerPane(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/public_html"))

	model = press(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	if !model.remote.sortDesc {
		t.Fatalf("'S' did not reverse the remote pane")
	}
	if model.local.sortDesc {
		t.Fatalf("'S' on the remote pane also flipped the local pane")
	}
}

func TestSortSurvivesNavigationUnlikeFilter(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/public_html"))
	model = press(t, model, runes("s")) // -> size
	model = press(t, model, runes("s")) // -> date
	key := model.remote.sortKey

	model = settle(t, model, model.navigateTo(paneRemote, "/public_html/assets"))
	if model.remote.sortKey != key {
		t.Fatalf("sort key reset on navigation: got %v, want %v", model.remote.sortKey, key)
	}
	// assets holds bundle.js, logo.png, manifest.json — by date the fake
	// tree has bundle.js newest; ascending puts it last.
	names := paneEntryNames(model.remote)
	if names[len(names)-1] != "bundle.js" {
		t.Fatalf("date sort not applied after navigation: %v", names)
	}
}

func TestSortReappliesAfterRefresh(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/public_html"))
	model = press(t, model, runes("s")) // size asc
	model = press(t, model, runes("S")) // size desc
	model = press(t, model, runes("r")) // refresh

	names := paneEntryNames(model.remote)
	// Dirs first, then files largest→smallest: index.html (48k) before
	// app.css (18k) before robots.txt (87).
	var files []string
	for _, n := range names {
		switch n {
		case "index.html", "app.css", "robots.txt":
			files = append(files, n)
		}
	}
	eq(t, files, []string{"index.html", "app.css", "robots.txt"})
}

func TestResortKeepsCursorOnSameEntry(t *testing.T) {
	pane := filePane{
		selected:   map[string]bool{},
		allEntries: mixedEntries(),
	}
	pane.entries = pane.allEntries
	sortEntries(pane.allEntries, sortByName, false)
	// name asc: delta, zeta, alpha.go, beta.txt, gamma.md — put cursor on beta.txt
	pane.cursor = 3
	if got, _ := pane.current(); got.Name != "beta.txt" {
		t.Fatalf("setup cursor on %q, want beta.txt", got.Name)
	}
	pane.sortKey = sortBySize
	pane.resort(20)
	if got, _ := pane.current(); got.Name != "beta.txt" {
		t.Fatalf("after resort cursor on %q, want it to follow beta.txt", got.Name)
	}
}
