// Package config loads and saves the user's TideFTP settings and resolves the
// per-user directories the app keeps its files under.
//
// It is a leaf package: it knows nothing about the UI or the protocol
// adapters, only the TOML file it reads and writes. Profile mirrors
// session.Target's shape rather than importing it, so this package stays free
// of that dependency; internal/ui converts between the two.
package config

import (
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"
)

// Config is the persisted application settings. Every field has a default in
// Default, and Load layers a TOML file over those defaults, so a partial or
// hand-edited file is fine.
type Config struct {
	Theme       string `toml:"theme"`
	Density     string `toml:"density"`
	Shadow      bool   `toml:"shadow"`
	ShowIcons   bool   `toml:"show_icons"`
	MaxParallel int    `toml:"max_parallel"`
	// Editor is the command the `e` action opens files with. Empty means
	// auto: $VISUAL, $EDITOR, git's core.editor, then a common editor on
	// PATH. A value may carry flags, e.g. "code -w".
	Editor   string    `toml:"editor,omitempty"`
	Layout   Layout    `toml:"layout"`
	Profiles []Profile `toml:"profiles"`
}

// Layout records the pane split ratios as fractions of the terminal. The UI
// clamps them back into its pane bounds on load, so an out-of-range value in
// the file cannot break the layout.
type Layout struct {
	FileSplit   float64 `toml:"file_split"`
	BottomSplit float64 `toml:"bottom_split"`
}

// Profile is a saved connection target: where to connect and as whom.
// Credentials are deliberately absent — see Known Gaps in docs/handoff.md.
type Profile struct {
	Name      string `toml:"name"`
	Protocol  string `toml:"protocol"`
	Host      string `toml:"host"`
	Port      int    `toml:"port"`
	User      string `toml:"user"`
	StartPath string `toml:"start_path"`
	// HostKeyPolicy is SFTP-only: "" (ask), "strict", or "off". Omitted from
	// the file when it is the ask default.
	HostKeyPolicy string `toml:"host_key_policy,omitempty"`
}

// SaveFunc persists a Config. It is a seam so callers — the UI — never have
// to know where config lives on disk, which keeps the filesystem out of the
// view layer and makes persistence easy to stub in tests.
type SaveFunc func(Config) error

// Default returns the settings used when no config file exists yet. It must
// stay in step with the values ui.NewModel would otherwise pick, so a first
// run looks identical to a run that later saved these same values.
func Default() Config {
	return Config{
		Theme:       "tide-night",
		Density:     "compact",
		Shadow:      true,
		ShowIcons:   true,
		MaxParallel: 2,
		Layout:      Layout{FileSplit: 0.5, BottomSplit: 0.28},
	}
}

// Load reads the config file at path. A missing or unparseable file yields the
// defaults rather than an error, so a deleted or corrupt config never stops
// the app from starting; only an unexpected I/O failure (a permissions
// problem, say) is reported.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), nil
		}
		return Config{}, err
	}
	cfg := Default()
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Default(), nil // corrupt config: start over rather than crash
	}
	return cfg, nil
}

// Save writes cfg to path, creating the directory as needed and replacing the
// file atomically so a crash mid-write cannot leave a truncated config behind.
func Save(path string, cfg Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
