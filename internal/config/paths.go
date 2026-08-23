package config

import (
	"os"
	"path/filepath"
)

// appName is the directory name the app uses under each XDG root.
const appName = "tideftp"

// Paths holds the three XDG directories the app uses. Only Config has a
// consumer today; State and Cache are resolved now so later work can adopt
// them without further plumbing, but nothing writes to them yet.
type Paths struct {
	Config string
	State  string
	Cache  string
}

// Resolve returns the app's XDG directories, honouring the XDG_* environment
// variables and falling back to sensible defaults when they are unset.
func Resolve() Paths {
	return Paths{
		Config: configDir(),
		State:  stateDir(),
		Cache:  cacheDir(),
	}
}

// ConfigPath is the full path to the config file.
func ConfigPath() string {
	return filepath.Join(configDir(), "config.toml")
}

func configDir() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, appName)
	}
	if v, err := os.UserConfigDir(); err == nil {
		return filepath.Join(v, appName)
	}
	return filepath.Join(".config", appName)
}

func stateDir() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, appName)
	}
	// os has no UserStateDir; the XDG state root is ~/.local/state.
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", appName)
	}
	return filepath.Join(".local", "state", appName)
}

func cacheDir() string {
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(v, appName)
	}
	if v, err := os.UserCacheDir(); err == nil {
		return filepath.Join(v, appName)
	}
	return filepath.Join(".cache", appName)
}
