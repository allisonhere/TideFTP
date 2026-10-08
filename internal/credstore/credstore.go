// Package credstore persists a connection's password in the OS keyring,
// opt-in and per profile. It exists because session.Credentials is
// deliberately kept out of config.Profile (see that type's doc comment) —
// a password still needs somewhere to live for the user who chooses to have
// one remembered, and that somewhere is the platform keyring, not
// config.toml.
package credstore

import "fmt"

// Key is the opaque key a stored password lives under, derived from the parts
// that name an account on a server: protocol, host, port and user together.
// It deliberately excludes a profile's Name and StartPath, so renaming a saved
// profile never orphans its stored password. Every caller that reads or writes
// the keyring must build the key through this one function, or the app and the
// non-interactive CLI would look up different keys for the same server.
func Key(protocol, host string, port int, user string) string {
	return fmt.Sprintf("%s|%s|%d|%s", protocol, host, port, user)
}

// Store gets, sets, and deletes a password by an opaque key. internal/ui
// builds that key from a profile's protocol/host/port/user, never its name,
// so renaming a saved profile does not orphan its stored password.
//
// A nil Store means the feature is unavailable — internal/ui treats that the
// same way it treats a nil config.SaveFunc: silently skip it rather than
// erroring, and hide the UI that would use it.
type Store interface {
	// Get reports the stored password for key, if any. ok is false with a
	// nil err when nothing is stored, not an error — that's the ordinary
	// case for a profile nobody chose to remember.
	Get(key string) (password string, ok bool, err error)
	// Set stores password for key, overwriting whatever was there. Setting
	// an empty password is the same as Delete: there is nothing useful to
	// remember about an empty secret.
	Set(key, password string) error
	// Delete removes whatever is stored for key. It is not an error for
	// nothing to have been stored.
	Delete(key string) error
}
