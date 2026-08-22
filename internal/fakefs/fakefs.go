package fakefs

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"tideftp/internal/domain"
	"tideftp/internal/vfs"
)

var _ vfs.FS = (*Remote)(nil)

type Remote struct {
	entries map[string][]domain.Entry
	// latency stands in for network round-trip time. Zero in tests; the app
	// sets a small one so the panes' loading state is visible in real use.
	latency time.Duration
}

// NewRemote builds the fake tree with no artificial latency.
func NewRemote() *Remote { return NewRemoteWithLatency(0) }

// NewRemoteWithLatency builds the fake tree, delaying every listing by the
// given duration so the UI's loading path gets exercised by hand.
func NewRemoteWithLatency(latency time.Duration) *Remote {
	now := time.Now()
	return &Remote{latency: latency, entries: map[string][]domain.Entry{
		"/": {
			dir("public_html", now.Add(-72*time.Hour)),
			dir("releases", now.Add(-24*time.Hour)),
			dir("incoming", now.Add(-3*time.Hour)),
			file("welcome.txt", 2688, now.Add(-6*time.Hour)),
			file(".server-note", 420, now.Add(-30*time.Hour)),
		},
		"/public_html": {
			dir("assets", now.Add(-20*time.Hour)),
			dir("uploads", now.Add(-9*time.Hour)),
			file("index.html", 48212, now.Add(-2*time.Hour)),
			file("app.css", 18420, now.Add(-2*time.Hour)),
			file("robots.txt", 87, now.Add(-400*time.Hour)),
		},
		"/public_html/assets": {
			file("logo.png", 348801, now.Add(-9*time.Hour)),
			file("bundle.js", 908122, now.Add(-90*time.Minute)),
			file("manifest.json", 1584, now.Add(-2*time.Hour)),
		},
		"/public_html/uploads": {
			file("hero-ocean.jpg", 2801448, now.Add(-7*time.Hour)),
			file("catalog.pdf", 1320028, now.Add(-8*time.Hour)),
		},
		"/releases": {
			dir("2026-08-ship", now.Add(-24*time.Hour)),
			file("deploy-notes.md", 6240, now.Add(-24*time.Hour)),
		},
		"/releases/2026-08-ship": {
			file("tideftp-linux-amd64.tar.gz", 6812440, now.Add(-23*time.Hour)),
			file("checksums.txt", 830, now.Add(-23*time.Hour)),
		},
		"/incoming": {
			file("client-drop.zip", 8214400, now.Add(-18*time.Minute)),
			file(".partial-upload", 32768, now.Add(-4*time.Minute)),
		},
	}}
}

func (r *Remote) List(ctx context.Context, dirPath string, showHidden bool) ([]domain.Entry, error) {
	if r.latency > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(r.latency):
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dirPath = clean(dirPath)
	stored, ok := r.entries[dirPath]
	if !ok {
		// A real server answers a bad path with an error, not an empty
		// directory, so the fake does too.
		return nil, fmt.Errorf("no such directory: %s", dirPath)
	}
	entries := append([]domain.Entry(nil), stored...)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind == domain.EntryDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	if showHidden {
		return entries, nil
	}
	filtered := entries[:0]
	for _, entry := range entries {
		if !entry.Hidden {
			filtered = append(filtered, entry)
		}
	}
	return filtered, nil
}

func (r *Remote) Child(current, name string) string {
	return clean(path.Join(clean(current), name))
}

func (r *Remote) Parent(current string) string {
	parent := path.Dir(clean(current))
	if parent == "." {
		return "/"
	}
	return parent
}

func clean(value string) string {
	if strings.TrimSpace(value) == "" {
		return "/"
	}
	return path.Clean("/" + strings.TrimPrefix(value, "/"))
}

func dir(name string, modified time.Time) domain.Entry {
	return domain.Entry{Name: name, Kind: domain.EntryDir, Mode: "drwxr-xr-x", Modified: modified, Hidden: strings.HasPrefix(name, ".")}
}

func file(name string, size int64, modified time.Time) domain.Entry {
	return domain.Entry{Name: name, Kind: domain.EntryFile, Size: size, Mode: "-rw-r--r--", Modified: modified, Hidden: strings.HasPrefix(name, ".")}
}
