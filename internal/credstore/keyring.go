package credstore

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// service names every secret this app stores in the platform keyring —
// macOS Keychain, Windows Credential Manager, or Linux Secret Service.
const service = "tideftp"

// Keyring is the real Store, backed by the OS keyring. It has no state of
// its own; every call reaches the platform keyring directly.
type Keyring struct{}

// New returns the real, OS-keyring-backed Store.
func New() Keyring { return Keyring{} }

func (Keyring) Get(key string) (string, bool, error) {
	password, err := keyring.Get(service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return password, true, nil
}

func (k Keyring) Set(key, password string) error {
	if password == "" {
		return k.Delete(key)
	}
	return keyring.Set(service, key, password)
}

func (Keyring) Delete(key string) error {
	err := keyring.Delete(service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
