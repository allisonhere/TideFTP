package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
)

// paneEntryNames is the visible entry names in a pane, in order.
func paneEntryNames(p filePane) []string {
	names := make([]string, len(p.entries))
	for i, e := range p.entries {
		names[i] = e.Name
	}
	return names
}

// filteredRemote returns a model connected with the remote pane focused and
// sitting in /public_html, where the fake tree holds assets/, uploads/,
// index.html, app.css, and robots.txt.
func filteredRemote(t *testing.T) Model {
	t.Helper()
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusRemote
	model = settle(t, model, model.navigateTo(paneRemote, "/public_html"))
	if len(model.remote.entries) != 5 {
		t.Fatalf("setup: /public_html has %d entries, want 5", len(model.remote.entries))
	}
	return model
}

func TestFilterNarrowsBySubstring(t *testing.T) {
	model := filteredRemote(t)

	model = press(t, model, runes("/"))
	if !model.remote.filtering {
		t.Fatalf("'/' did not open the filter input")
	}
	model = press(t, model, runes("css"))

	if got := model.remote.filter; got != "css" {
		t.Fatalf("filter query = %q, want %q", got, "css")
	}
	names := paneEntryNames(model.remote)
	if len(names) != 1 || names[0] != "app.css" {
		t.Fatalf("filtered entries = %v, want [app.css]", names)
	}
	if len(model.remote.allEntries) != 5 {
		t.Fatalf("allEntries = %d, want the full 5 kept behind the filter", len(model.remote.allEntries))
	}
}

func TestFilterMatchesGlobWhenQueryHasMeta(t *testing.T) {
	model := filteredRemote(t)

	model = press(t, model, runes("/"))
	model = press(t, model, runes("*.txt"))

	names := paneEntryNames(model.remote)
	if len(names) != 1 || names[0] != "robots.txt" {
		t.Fatalf("glob '*.txt' matched %v, want [robots.txt]", names)
	}
}

func TestFilterKeystrokesDoNotFireBindings(t *testing.T) {
	model := filteredRemote(t)
	engine := model.engine.(*scriptedEngine)

	model = press(t, model, runes("/"))
	model = press(t, model, runes("u")) // 'u' is the upload binding

	if model.remote.filter != "u" {
		t.Fatalf("filter query = %q, want the literal %q", model.remote.filter, "u")
	}
	if len(engine.started) != 0 {
		t.Fatalf("typing into the filter queued %d transfer(s)", len(engine.started))
	}
}

func TestFilterEnterAcceptsAndKeepsIt(t *testing.T) {
	model := filteredRemote(t)

	model = press(t, model, runes("/"))
	model = press(t, model, runes("asset"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if model.remote.filtering {
		t.Fatalf("enter did not close the filter input")
	}
	if model.remote.filter != "asset" {
		t.Fatalf("enter dropped the filter: query = %q", model.remote.filter)
	}
	if names := paneEntryNames(model.remote); len(names) != 1 || names[0] != "assets" {
		t.Fatalf("accepted filter shows %v, want [assets]", names)
	}
}

func TestFilterEscClearsAndRestores(t *testing.T) {
	model := filteredRemote(t)

	model = press(t, model, runes("/"))
	model = press(t, model, runes("asset"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEsc})

	if model.remote.filterActive() {
		t.Fatalf("esc left the filter active: %+v", model.remote.filter)
	}
	if len(model.remote.entries) != 5 {
		t.Fatalf("esc restored %d entries, want the full 5", len(model.remote.entries))
	}
}

func TestFilterEmptyEnterClearsIt(t *testing.T) {
	model := filteredRemote(t)

	model = press(t, model, runes("/"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if model.remote.filterActive() {
		t.Fatalf("accepting an empty query left the filter active")
	}
}

func TestFilterClearedWhenPaneNavigates(t *testing.T) {
	model := filteredRemote(t)

	model = press(t, model, runes("/"))
	model = press(t, model, runes("asset"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	// The cursor is on "assets" (the only match); Enter opens it.
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if model.remote.path != "/public_html/assets" {
		t.Fatalf("did not walk into the match: path = %q", model.remote.path)
	}
	if model.remote.filterActive() {
		t.Fatalf("filter survived a directory change: %q", model.remote.filter)
	}
}

func TestFilterSurvivesRefresh(t *testing.T) {
	model := filteredRemote(t)

	model = press(t, model, runes("/"))
	model = press(t, model, runes("css"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = press(t, model, runes("r")) // refresh

	if model.remote.filter != "css" {
		t.Fatalf("refresh dropped the filter: query = %q", model.remote.filter)
	}
	if names := paneEntryNames(model.remote); len(names) != 1 || names[0] != "app.css" {
		t.Fatalf("filter not reapplied after refresh: %v", names)
	}
}

func TestFilterAlwaysKeepsParentEntry(t *testing.T) {
	model := loadedModel(t, newScriptedEngine())
	model.focus = focusLocal
	if len(model.local.entries) == 0 || model.local.entries[0].Name != parentEntryName {
		t.Fatalf("setup: local pane has no %q row", parentEntryName)
	}

	model = press(t, model, runes("/"))
	model = press(t, model, runes("zzz-no-such-file"))

	names := paneEntryNames(model.local)
	if len(names) != 1 || names[0] != parentEntryName {
		t.Fatalf("a no-match filter left %v, want just [%q]", names, parentEntryName)
	}
}

func TestEntryMatchesFilter(t *testing.T) {
	cases := []struct {
		name, query string
		want        bool
	}{
		{"app.css", "css", true},
		{"app.css", "CSS", true},
		{"README.md", "css", false},
		{"robots.txt", "*.txt", true},
		{"robots.txt", "*.md", false},
		{"photo.JPG", "*.jpg", true},
		{"anything", "", true},
		{parentEntryName, "nomatch", true},
		{"a[b", "a[b", true}, // invalid glob falls back to substring
	}
	for _, c := range cases {
		if got := entryMatchesFilter(c.name, c.query); got != c.want {
			t.Errorf("entryMatchesFilter(%q, %q) = %v, want %v", c.name, c.query, got, c.want)
		}
	}
}

func TestFilterHintShowsQueryAndCount(t *testing.T) {
	pane := filePane{
		allEntries: []domain.Entry{{Name: "a.go"}, {Name: "b.go"}, {Name: "c.txt"}},
		filter:     "go",
		filtering:  true,
	}
	pane.applyFilter(20)
	hint := Model{}.paneHint(pane, "/some/path")
	if want := "filter: go_  2/3"; hint != want {
		t.Fatalf("paneHint = %q, want %q", hint, want)
	}
}
