//go:build unix

package main

import (
	"os"
	"syscall"
)

// execRestart replaces this process with the freshly installed binary,
// keeping the same arguments and environment. Replacing the image rather than
// spawning a child means there is never a moment where two TideFTPs share the
// terminal, and the shell that launched the original still waits on the one
// process it knows about.
func execRestart(path string) error {
	return syscall.Exec(path, append([]string{path}, os.Args[1:]...), os.Environ())
}
