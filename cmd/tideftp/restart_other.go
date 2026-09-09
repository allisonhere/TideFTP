//go:build !unix

package main

import "errors"

// execRestart has no implementation off Unix: syscall.Exec does not exist
// there. Releases are linux/darwin only, so this exists to keep the package
// compiling under GOOS=windows rather than to be reached.
func execRestart(string) error {
	return errors.New("restarting in place is not supported on this platform")
}
