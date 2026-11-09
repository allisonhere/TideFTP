package ui

import (
	"context"

	"tideftp/internal/domain"
	"tideftp/internal/vfs"
)

// destLister lists destination directories for conflict checks, telling a
// directory that does not exist yet — nothing there can conflict — apart
// from one that exists but could not be listed, where treating it as empty
// would queue every file as new and overwrite whatever is already there,
// regardless of the "If file exists" policy.
//
// Protocols do not agree on the error for a missing directory (FTP has only
// a 550 reply), so missing is decided structurally instead: a directory is
// missing when its parent lists fine and does not contain it, or when its
// parent is itself missing.
type destLister struct {
	ctx   context.Context
	fs    vfs.FS
	cache map[string]destListing
}

type destListing struct {
	entries []domain.Entry
	err     error
}

func newDestLister(ctx context.Context, fs vfs.FS) *destLister {
	return &destLister{ctx: ctx, fs: fs, cache: map[string]destListing{}}
}

// once lists dir a single time, caching the outcome. A missing directory can
// take a full timeout to fail over FTP, so it is not retried here.
func (l *destLister) once(dir string) ([]domain.Entry, error) {
	if got, ok := l.cache[dir]; ok {
		return got.entries, got.err
	}
	ctx, cancel := context.WithTimeout(l.ctx, listTimeout)
	entries, err := l.fs.List(ctx, dir, true)
	cancel()
	l.cache[dir] = destListing{entries, err}
	return entries, err
}

// list returns dir's entries. exists is false, with a nil error, when dir is
// known not to exist; a non-nil error means existence could not be settled.
func (l *destLister) list(dir string) (entries []domain.Entry, exists bool, err error) {
	entries, err = l.once(dir)
	if err == nil {
		return entries, true, nil
	}
	missing, known := l.missing(dir)
	if known && missing {
		return nil, false, nil
	}
	if known && !missing {
		// The parent shows it, so the failure was not "does not exist":
		// give it the retries an ordinary scan listing gets.
		entries, retryErr := listWithRetry(l.ctx, l.fs, dir, true)
		l.cache[dir] = destListing{entries, retryErr}
		if retryErr == nil {
			return entries, true, nil
		}
		return nil, false, retryErr
	}
	return nil, false, err
}

// missing reports whether dir is known to be absent. known is false when no
// ancestor could be listed to say either way.
func (l *destLister) missing(dir string) (missing, known bool) {
	parent := l.fs.Parent(dir)
	if parent == dir || parent == "" {
		return false, false
	}
	siblings, err := l.once(parent)
	if err != nil {
		if parentMissing, parentKnown := l.missing(parent); parentKnown && parentMissing {
			return true, true
		}
		return false, false
	}
	for _, e := range siblings {
		if l.fs.Child(parent, e.Name) == dir {
			return false, true
		}
	}
	return true, true
}
