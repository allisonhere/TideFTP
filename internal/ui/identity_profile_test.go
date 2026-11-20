package ui

import (
	"path/filepath"
	"testing"

	"tideftp/internal/config"
	"tideftp/internal/fakefs"
	"tideftp/internal/localfs"
	"tideftp/internal/session"
)

func TestSavedIdentitySurvivesRestartAndDirectConnect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, nil, config.Default(), func(c config.Config) error { return config.Save(path, c) }, nil, "")
	model.openConnectFormFor(testTarget, connectFieldIdentity)
	model.connectForm.identity = " /home/alex/.ssh/custom_key "
	model.connectForm.keyPassphrase = "never-save-this"
	model = settle(t, model, model.saveConnectProfile())
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	profiles := profilesFromConfig(cfg.Profiles)
	if len(profiles) != 1 || profiles[0].IdentityFile != "/home/alex/.ssh/custom_key" {
		t.Fatalf("reloaded profiles = %+v", profiles)
	}
	restarted := NewModel(localfs.New(), dialer, nil, cfg, nil, nil, "")
	restarted.openConnectFormFor(profiles[0], connectFieldIdentity)
	if restarted.connectForm.identity != profiles[0].IdentityFile || restarted.connectForm.keyPassphrase != "" {
		t.Fatal("reopened form lost identity or retained passphrase")
	}
	// Direct server-list connections supply empty credentials for key auth.
	restarted.connectFor(profiles[0], session.Credentials{})
	if restarted.lastCreds.IdentityFile != profiles[0].IdentityFile {
		t.Fatalf("direct connection credentials = %+v", restarted.lastCreds)
	}
	restarted.connectForm.identity = ""
	cleared, ok := restarted.targetFromForm()
	if !ok || cleared.IdentityFile != "" {
		t.Fatal("cannot clear saved identity")
	}
}
