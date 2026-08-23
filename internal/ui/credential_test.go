package ui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"tideftp/internal/config"
	"tideftp/internal/fakecredstore"
	"tideftp/internal/fakefs"
	"tideftp/internal/localfs"
	"tideftp/internal/session"
)

// credTestTarget is FTP, not SFTP: FTP always authenticates with a password,
// so Password (and Remember, once a credstore is wired in) are visible from
// the moment the form opens, with no Auth-mode key presses needed first.
var credTestTarget = session.Target{Name: "ftpbox", Protocol: "ftp", Host: "ftp.example.com", Port: 21, User: "alice", StartPath: "/incoming"}

func TestConnectFormSavingRemembersThePassword(t *testing.T) {
	store := fakecredstore.New()
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, []session.Target{credTestTarget}, config.Default(), nil, store)
	model.width, model.height = 120, 36

	model = press(t, model, runes("c"))
	if !model.connectFieldVisible(connectFieldRemember) {
		t.Fatalf("Remember should be visible once a credstore is wired in")
	}

	model.connectForm.password = "s3cret"
	model.connectForm.remember = true
	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlS})

	if len(model.profiles) != 1 {
		t.Fatalf("profiles = %d, want 1", len(model.profiles))
	}
	password, ok, err := store.Get(credentialKey(model.profiles[0]))
	if err != nil || !ok {
		t.Fatalf("store.Get after save = %q, %v, %v; want the saved password", password, ok, err)
	}
	if password != "s3cret" {
		t.Fatalf("stored password = %q, want %q", password, "s3cret")
	}
}

func TestConnectFormReopeningPrefillsARememberedPassword(t *testing.T) {
	store := fakecredstore.New()
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, []session.Target{credTestTarget}, config.Default(), nil, store)
	model.width, model.height = 120, 36

	model = press(t, model, runes("c"))
	model.connectForm.password = "s3cret"
	model.connectForm.remember = true
	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlS})

	// Close and reopen: the form starts from scratch, and the password must
	// come back from the credstore rather than needing to be retyped.
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.overlay != overlayNone {
		t.Fatalf("esc did not close the form")
	}
	model = press(t, model, runes("c"))

	if model.connectForm.password != "s3cret" {
		t.Fatalf("password on reopen = %q, want the remembered one", model.connectForm.password)
	}
	if !model.connectForm.remember {
		t.Fatalf("remember on reopen = false, want true (a password is stored)")
	}
}

func TestConnectFormNotRememberingForgetsAnyStoredPassword(t *testing.T) {
	store := fakecredstore.New()
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, []session.Target{credTestTarget}, config.Default(), nil, store)
	model.width, model.height = 120, 36

	// Save once with Remember on, so there is something to forget.
	model = press(t, model, runes("c"))
	model.connectForm.password = "s3cret"
	model.connectForm.remember = true
	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlS})

	key := credentialKey(model.profiles[0])
	if _, ok, _ := store.Get(key); !ok {
		t.Fatalf("setup: password was not stored")
	}

	// Turn Remember back off and save again.
	model.connectForm.remember = false
	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlS})

	if _, ok, _ := store.Get(key); ok {
		t.Fatalf("password still stored after saving with Remember off")
	}
}

func TestConnectFormDeletingProfileForgetsItsPassword(t *testing.T) {
	store := fakecredstore.New()
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, []session.Target{credTestTarget}, config.Default(), nil, store)
	model.width, model.height = 120, 36

	model = press(t, model, runes("c"))
	model.connectForm.password = "s3cret"
	model.connectForm.remember = true
	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlS})

	key := credentialKey(model.profiles[0])
	if _, ok, _ := store.Get(key); !ok {
		t.Fatalf("setup: password was not stored")
	}

	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlX})

	if len(model.profiles) != 0 {
		t.Fatalf("profiles = %d after delete, want 0", len(model.profiles))
	}
	if _, ok, _ := store.Get(key); ok {
		t.Fatalf("password still stored after deleting the profile it belonged to")
	}
}

func TestConnectFormCredentialStoreErrorsSurfaceWithoutCrashing(t *testing.T) {
	store := fakecredstore.New()
	store.Err = errors.New("secret service not running")
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, []session.Target{credTestTarget}, config.Default(), nil, store)
	model.width, model.height = 120, 36

	model = press(t, model, runes("c"))
	if !model.statusErr || !strings.Contains(model.status, "load saved password") {
		t.Fatalf("status = %q (err=%v), want a load-password error", model.status, model.statusErr)
	}

	model.connectForm.password = "s3cret"
	model.connectForm.remember = true
	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlS})

	if !model.statusErr || !strings.Contains(model.status, "password not saved") {
		t.Fatalf("status = %q (err=%v), want a save-password error", model.status, model.statusErr)
	}
	if len(model.profiles) != 1 {
		t.Fatalf("the profile itself should still save even though remembering its password failed")
	}
}

func TestConnectFormRememberFieldRenders(t *testing.T) {
	store := fakecredstore.New()
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, []session.Target{credTestTarget}, config.Default(), nil, store)
	model.width, model.height = 120, 36

	model = press(t, model, runes("c"))
	plain := ansi.Strip(model.View())
	if !strings.Contains(plain, "Remember") {
		t.Fatalf("connect form is missing the Remember field:\n%s", plain)
	}
}

func TestConnectFormRememberHiddenWithoutACredStore(t *testing.T) {
	dialer := &stubDialer{fs: fakefs.NewRemote(), engine: newScriptedEngine()}
	model := NewModel(localfs.New(), dialer, []session.Target{credTestTarget}, config.Default(), nil, nil)
	model.width, model.height = 120, 36

	model = press(t, model, runes("c"))
	if model.connectFieldVisible(connectFieldRemember) {
		t.Fatalf("Remember must stay hidden when no credstore is wired in")
	}
}
