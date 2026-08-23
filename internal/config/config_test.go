package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultValues(t *testing.T) {
	cfg := Default()
	if cfg.Theme != "tide-night" {
		t.Fatalf("default theme = %q, want tide-night", cfg.Theme)
	}
	if cfg.Density != "compact" {
		t.Fatalf("default density = %q, want compact", cfg.Density)
	}
	if !cfg.Shadow || !cfg.ShowIcons {
		t.Fatalf("shadow=%v showIcons=%v, want both true", cfg.Shadow, cfg.ShowIcons)
	}
	if cfg.MaxParallel != 2 {
		t.Fatalf("default maxParallel = %d, want 2", cfg.MaxParallel)
	}
	if cfg.Layout.FileSplit != 0.5 || cfg.Layout.BottomSplit != 0.28 {
		t.Fatalf("default layout = %+v, want 0.5/0.28", cfg.Layout)
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if cfg != Default() {
		t.Fatalf("Load on missing file = %+v, want defaults %+v", cfg, Default())
	}
}

func TestLoadCorruptFileReturnsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("this is not [ valid toml"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load on corrupt file should not error, got %v", err)
	}
	if cfg != Default() {
		t.Fatalf("Load on corrupt file = %+v, want defaults %+v", cfg, Default())
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	want := Config{
		Theme:       "nord",
		Density:     "comfortable",
		Shadow:      false,
		ShowIcons:   false,
		MaxParallel: 4,
		Layout:      Layout{FileSplit: 0.63, BottomSplit: 0.21},
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestSaveCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "config.toml")
	if err := Save(path, Default()); err != nil {
		t.Fatalf("Save into missing parent dirs: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("saved file missing: %v", err)
	}
}

func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := Save(path, Default()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// No leftover temp files after a successful save.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.toml" {
		t.Fatalf("directory has %v, want only config.toml", entries)
	}
}

// The next two tests assert the Unix XDG layout; on other platforms the
// stdlib helpers return platform-specific roots, so they are Unix-only.

func TestResolveHonoursXDGEnvironment(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg/config")
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg/state")
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg/cache")

	got := Resolve()
	want := Paths{
		Config: "/tmp/xdg/config/tideftp",
		State:  "/tmp/xdg/state/tideftp",
		Cache:  "/tmp/xdg/cache/tideftp",
	}
	if got != want {
		t.Fatalf("Resolve = %+v, want %+v", got, want)
	}
	if ConfigPath() != "/tmp/xdg/config/tideftp/config.toml" {
		t.Fatalf("ConfigPath = %q", ConfigPath())
	}
}

func TestResolveFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")

	got := Resolve()
	want := Paths{
		Config: filepath.Join(home, ".config", "tideftp"),
		State:  filepath.Join(home, ".local", "state", "tideftp"),
		Cache:  filepath.Join(home, ".cache", "tideftp"),
	}
	if got != want {
		t.Fatalf("Resolve = %+v, want %+v", got, want)
	}
}
