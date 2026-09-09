// Package config loads and saves the user's TideFTP settings and resolves the
// per-user directories the app keeps its files under.
//
// It is a leaf package: it knows nothing about the UI or the protocol
// adapters, only the TOML file it reads and writes. Profile mirrors
// session.Target's shape rather than importing it, so this package stays free
// of that dependency; internal/ui converts between the two.
package config

import (
	"errors"
	"fmt"
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
	Editor string `toml:"editor,omitempty"`
	// VerifyChecksums re-reads both ends of every completed transfer and
	// compares SHA-256 sums. Off by default: it is correct, and it doubles
	// what a transfer costs.
	VerifyChecksums bool `toml:"verify_checksums"`
	// AutoReconnect redials, with backoff, after a connection drops on its
	// own. It never fires for a disconnect the user asked for.
	AutoReconnect bool      `toml:"auto_reconnect"`
	Layout        Layout    `toml:"layout"`
	Sort          Sort      `toml:"sort"`
	Updates       Updates   `toml:"updates"`
	Profiles      []Profile `toml:"profiles"`
}

// Updates is the self-update behaviour: whether to look for a newer release
// on launch, and what the last look found.
//
// There is deliberately no cache of the available version here. The notice is
// check-first — it is only ever raised by a live check in the running
// session, never rehydrated from disk — so a release that was available last
// week cannot resurface as a phantom update after it has been installed or
// yanked. LastCheckedUnix and DismissedVersion are the only two facts worth
// carrying across runs, and neither of them can claim an update exists.
type Updates struct {
	// CheckOnStartup looks for a newer release when the app launches. It is
	// the only gate on the automatic check; there is no interval, because a
	// check is one request per launch and a long-running session that has
	// already checked has nothing to gain from checking again.
	CheckOnStartup bool `toml:"check_on_startup"`
	// LastCheckedUnix is when a check last completed, successfully or not.
	// Shown in Settings; never used to decide whether to check.
	LastCheckedUnix int64 `toml:"last_checked_unix"`
	// DismissedVersion is a version the user asked not to be told about
	// again. It suppresses the notice for that exact version only, so a
	// later release still surfaces.
	DismissedVersion string `toml:"dismissed_version,omitempty"`
}

// Sort is the default order both file panes open in. Key is one of "name",
// "size", "date", or "type"; an unrecognised value is treated as "name" by
// the UI. Desc reverses it. Panes can be re-sorted independently at runtime
// without changing what is saved here.
type Sort struct {
	Key  string `toml:"key"`
	Desc bool   `toml:"desc"`
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
		Theme:         "tide-night",
		Density:       "compact",
		Shadow:        true,
		ShowIcons:     true,
		MaxParallel:   2,
		AutoReconnect: true,
		Layout:        Layout{FileSplit: 0.5, BottomSplit: 0.28},
		Sort:          Sort{Key: "name"},
		Updates:       Updates{CheckOnStartup: true},
	}
}

// ErrCorrupt wraps Load's error for a file that exists but is not valid TOML.
// It is distinct from a missing file on purpose: a file that is there and
// unreadable holds the user's saved profiles, and the app must not treat it
// as a blank slate it may overwrite. Callers check for it with errors.Is and
// run without persistence rather than saving defaults over the file — see
// main, which does exactly that.
var ErrCorrupt = errors.New("config file is not valid TOML")

// Load reads the config file at path. A missing file yields the defaults with
// no error, so a first run needs nothing on disk. A file that exists but does
// not parse yields the defaults *and* ErrCorrupt: the app can still start,
// but whatever is in that file has to survive, and the caller is the one that
// decides how (today: by not saving at all until the user fixes it).
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
		return Default(), fmt.Errorf("%w: %v", ErrCorrupt, err)
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
