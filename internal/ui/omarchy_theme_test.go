package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/allisonhere/tideui"

	"tideftp/internal/domain"
	"tideftp/internal/omarchy"
)

// hostileOmarchyPalettes are deliberately bad desktop palettes. Whatever
// Omarchy theme the user is on, omarchyThemeFromPalette + tideui.BuildStyles
// must still produce readable file/transfer rows.
var hostileOmarchyPalettes = map[string]omarchy.Palette{
	"near-black on black": {
		Mode: "dark", Background: "#000000", Foreground: "#050505",
		Accent: "#0a0a0a", Selection: "#111111", Muted: "#0d0d0d",
		StatusBg: "#000000", Error: "#1a0000", Ok: "#001a00",
	},
	"grey on grey": {
		Mode: "dark", Background: "#4a4a4a", Foreground: "#525252",
		Accent: "#555555", Selection: "#4f4f4f", Muted: "#505050",
		StatusBg: "#4b4b4b", Error: "#5a4a4a", Ok: "#4a5a4a",
	},
	"washed-out light": {
		Mode: "light", Background: "#fdfdfd", Foreground: "#efefef",
		Accent: "#f2f2f2", Selection: "#f5f5f5", Muted: "#f0f0f0",
		StatusBg: "#fcfcfc", Error: "#ffecec", Ok: "#ecffec",
	},
	"retro-82 (real dark)": {
		Mode: "dark", Background: "#05182e", Foreground: "#f6dcac",
		Accent: "#faa968", Selection: "#134e5a", Muted: "#2a6b78",
		StatusBg: "#0a2540", Error: "#f85525", Ok: "#028391",
	},
	"catppuccin-latte (real light)": {
		Mode: "light", Background: "#eff1f5", Foreground: "#4c4f69",
		Accent: "#1e66f5", Selection: "#bcc0cc", Muted: "#8c8fa1",
		StatusBg: "#e6e9ef", Error: "#d20f39", Ok: "#40a02b",
	},
}

func TestOmarchyThemeRowsAreReadable(t *testing.T) {
	kinds := map[string]domain.EntryKind{
		"file": domain.EntryFile, "dir": domain.EntryDir, "symlink": domain.EntrySymlink,
	}
	states := []struct {
		name           string
		cursor, marked bool
		hidden         bool
	}{
		{name: "plain"},
		{name: "cursor", cursor: true},
		{name: "marked", marked: true},
		{name: "hidden", hidden: true},
	}
	statuses := map[string]domain.TransferStatus{
		"queued": domain.Queued, "active": domain.Active, "failed": domain.Failed, "done": domain.Done,
	}

	for name, pal := range hostileOmarchyPalettes {
		pal := pal
		t.Run(name, func(t *testing.T) {
			theme := omarchyThemeFromPalette(pal)
			if theme.Name != themeNameMatchOmarchy {
				t.Fatalf("name = %q", theme.Name)
			}
			renderer := tideui.NewRenderer(theme, tideui.StyleOptions{Density: tideui.Compact})

			for kindName, kind := range kinds {
				for _, st := range states {
					entry := domain.Entry{Name: "sample", Kind: kind, Hidden: st.hidden}
					bg, fg, nm := entryPalette(renderer, entry, st.cursor, st.marked)
					floor := textMinContrast
					if st.hidden && !st.cursor && !st.marked {
						floor = dimMinContrast
					}
					label := name + "/" + kindName + "/" + st.name
					checkContrast(t, label+" meta", fg, bg, floor)
					checkContrast(t, label+" name", nm, bg, floor)
				}
			}
			for sName, status := range statuses {
				for _, cursor := range []bool{false, true} {
					bg, fg, accent := transferPalette(renderer, status, cursor)
					floor := textMinContrast
					if status == domain.Done && !cursor {
						floor = dimMinContrast
					}
					lbl := name + "/" + sName
					checkContrast(t, lbl+" text", fg, bg, floor)
					checkContrast(t, lbl+" accent", accent, bg, floor)
				}
			}
		})
	}
}

func TestOmarchyPickerEntryFallsBackWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("PATH", filepath.Join(dir, "no-bin"))
	forceOmarchyRefresh()

	if _, ok := resolveOmarchyTheme(); ok {
		t.Fatal("resolveOmarchyTheme should be ok=false with no Omarchy present")
	}
	entry := omarchyPickerEntry()
	if entry.Name != themeNameMatchOmarchy {
		t.Errorf("placeholder name = %q, want %q", entry.Name, themeNameMatchOmarchy)
	}
	if entry.Bg != tideNight.Bg {
		t.Errorf("placeholder should reuse tide-night colours, got Bg %s", entry.Bg)
	}
	if got := themeByName(themeNameMatchOmarchy); got.Name != themeNameMatchOmarchy {
		t.Errorf("themeByName(match-omarchy) = %q", got.Name)
	}
}

func TestOmarchyPickerEntryReadsStagedColorsTOML(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("PATH", filepath.Join(dir, "no-bin"))
	forceOmarchyRefresh()
	t.Cleanup(forceOmarchyRefresh)

	themeDir := filepath.Join(dir, "omarchy", "current", "theme")
	if err := os.MkdirAll(themeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	colors := "mode = \"dark\"\nbackground = \"#05182e\"\nforeground = \"#f6dcac\"\naccent = \"#faa968\"\nred = \"#f85525\"\ngreen = \"#028391\"\nmuted = \"#2a6b78\"\n"
	if err := os.WriteFile(filepath.Join(themeDir, "colors.toml"), []byte(colors), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "omarchy", "current", "theme.name"), []byte("retro-82\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := resolveOmarchyTheme()
	if !ok {
		t.Fatal("expected ok=true with a staged colors.toml")
	}
	if contrastRatio(got.Fg, got.Bg) < textMinContrast {
		t.Errorf("fg/bg contrast %.2f below %.1f", contrastRatio(got.Fg, got.Bg), textMinContrast)
	}
	if contrastRatio(got.StatusBar, got.Bg) < 1.12 {
		t.Errorf("status bar not distinct from background: %.2f", contrastRatio(got.StatusBar, got.Bg))
	}
}
