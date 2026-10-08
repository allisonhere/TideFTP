package cli

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"tideftp/internal/domain"
)

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// syncFilter decides which source files take part in a sync.
//
// Patterns are path.Match globs. One without a slash is matched against the
// file's name; one with a slash against its path relative to the sync root
// (a leading slash is ignored). Excludes win over includes; when any include
// is given, a file must match one. Directories are always walked unless an
// exclude names them, so `--include '*.go'` still finds nested files.
//
// Size and age limits apply to source files only. They deliberately do not
// shape what --delete may remove: a file that merely falls outside --max-age
// is "not selected", never "gone from the source".
type syncFilter struct {
	includes, excludes []string
	minSize, maxSize   int64 // 0 = unset
	minAge, maxAge     time.Duration
	now                time.Time
}

func (f syncFilter) active() bool {
	return len(f.includes) > 0 || len(f.excludes) > 0 ||
		f.minSize > 0 || f.maxSize > 0 || f.minAge > 0 || f.maxAge > 0
}

func patternMatches(pattern, rel string) bool {
	pattern = strings.TrimPrefix(pattern, "/")
	target := rel
	if !strings.Contains(pattern, "/") {
		target = path.Base(rel)
	}
	ok, _ := path.Match(pattern, target)
	return ok
}

// namePasses applies only the include/exclude patterns.
func (f syncFilter) namePasses(rel string) bool {
	for _, p := range f.excludes {
		if patternMatches(p, rel) {
			return false
		}
	}
	if len(f.includes) == 0 {
		return true
	}
	for _, p := range f.includes {
		if patternMatches(p, rel) {
			return true
		}
	}
	return false
}

// dirExcluded reports whether an exclude pattern names the directory itself,
// which prunes its whole subtree from the walk.
func (f syncFilter) dirExcluded(rel string) bool {
	for _, p := range f.excludes {
		if patternMatches(p, rel) {
			return true
		}
	}
	return false
}

// selects reports whether a source file is part of the sync.
func (f syncFilter) selects(rel string, e domain.Entry) bool {
	if !f.namePasses(rel) {
		return false
	}
	if f.minSize > 0 && e.Size < f.minSize {
		return false
	}
	if f.maxSize > 0 && e.Size > f.maxSize {
		return false
	}
	if !e.Modified.IsZero() {
		age := f.now.Sub(e.Modified)
		if f.minAge > 0 && age < f.minAge {
			return false
		}
		if f.maxAge > 0 && age > f.maxAge {
			return false
		}
	}
	return true
}

// parseSize reads "100", "10k", "1.5M", "2G" (powers of 1024).
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, nil
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'k':
		mult = 1 << 10
	case 'm':
		mult = 1 << 20
	case 'g':
		mult = 1 << 30
	case 't':
		mult = 1 << 40
	}
	if mult != 1 {
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return int64(n * float64(mult)), nil
}

// parseAge reads a Go duration plus "d" (days) and "w" (weeks) suffixes.
func parseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	unit := time.Duration(0)
	switch s[len(s)-1] {
	case 'd':
		unit = 24 * time.Hour
	case 'w':
		unit = 7 * 24 * time.Hour
	}
	if unit != 0 {
		n, err := strconv.ParseFloat(s[:len(s)-1], 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid age %q", s)
		}
		return time.Duration(n * float64(unit)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid age %q", s)
	}
	return d, nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
