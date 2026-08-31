package ui

import (
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
)

// parseChmodMode turns an octal string like "644", "0755", or "2775" into a
// FileMode carrying only permission and setuid/setgid/sticky bits. It is
// deliberately strict: a mode is a small, well-known thing to type, and a
// typo that parsed as something else would silently misset permissions.
func parseChmodMode(value string) (fs.FileMode, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("mode is required, e.g. 644")
	}
	n, err := strconv.ParseUint(value, 8, 32)
	if err != nil {
		return 0, errors.New("mode must be octal digits, e.g. 644 or 2775")
	}
	if n > 0o7777 {
		return 0, errors.New("mode out of range (max 7777)")
	}
	mode := fs.FileMode(n & 0o777)
	if n&0o4000 != 0 {
		mode |= fs.ModeSetuid
	}
	if n&0o2000 != 0 {
		mode |= fs.ModeSetgid
	}
	if n&0o1000 != 0 {
		mode |= fs.ModeSticky
	}
	return mode, nil
}

// modeStringToOctal converts a listing's mode string (fs.FileMode.String()
// output, e.g. "-rw-r--r--" or "drwxr-xr-x") to the "644" form the chmod
// prompt pre-fills. It reads only the trailing nine permission characters,
// so an entry whose mode column is a kind label ("dir", "file" — how the FTP
// adapter fills it) yields "", and the caller picks a sensible default.
func modeStringToOctal(mode string) string {
	if len(mode) < 9 {
		return ""
	}
	perm := mode[len(mode)-9:]
	var digits [3]int
	for i := 0; i < 9; i++ {
		if perm[i] == '-' {
			continue
		}
		switch i % 3 {
		case 0:
			digits[i/3] += 4
		case 1:
			digits[i/3] += 2
		case 2:
			digits[i/3] += 1
		}
	}
	return fmt.Sprintf("%d%d%d", digits[0], digits[1], digits[2])
}

// symbolicPerm renders a parsed mode as the nine-character "rw-r--r--" form,
// for the prompt to echo back what the octal being typed actually means.
func symbolicPerm(mode fs.FileMode) string {
	return mode.Perm().String()[1:]
}
