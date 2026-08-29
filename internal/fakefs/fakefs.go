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
	// contents holds file bodies for ReadFile/WriteFile, keyed by clean path.
	// A listed file with no entry here reads as empty.
	contents map[string][]byte
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
	return &Remote{latency: latency, contents: map[string][]byte{
		"/welcome.txt":              []byte("Welcome to the fake server.\n"),
		"/public_html/robots.txt":   []byte("User-agent: *\nDisallow:\n"),
		"/releases/deploy-notes.md": []byte("# Deploy notes\n\n- push the build\n- restart\n"),
	}, entries: map[string][]domain.Entry{
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

func (r *Remote) Mkdir(ctx context.Context, dirPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dirPath = clean(dirPath)
	if _, ok := r.entries[dirPath]; ok {
		return fmt.Errorf("%w: %s", vfs.ErrExists, dirPath)
	}
	parent := path.Dir(dirPath)
	name := path.Base(dirPath)
	children, ok := r.entries[parent]
	if !ok {
		return fmt.Errorf("no such directory: %s", parent)
	}
	now := time.Now()
	r.entries[parent] = append(children, dir(name, now))
	r.entries[dirPath] = []domain.Entry{}
	return nil
}

func (r *Remote) Rename(ctx context.Context, oldPath, newPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	oldPath, newPath = clean(oldPath), clean(newPath)
	if oldPath == newPath {
		return nil
	}
	oldParent, newParent := path.Dir(oldPath), path.Dir(newPath)
	oldName, newName := path.Base(oldPath), path.Base(newPath)
	children, ok := r.entries[oldParent]
	if !ok {
		return fmt.Errorf("no such directory: %s", oldParent)
	}
	idx := -1
	for i, entry := range children {
		if entry.Name == oldName {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("no such item: %s", oldPath)
	}
	dstSiblings, ok := r.entries[newParent]
	if !ok {
		return fmt.Errorf("no such directory: %s", newParent)
	}
	for _, entry := range dstSiblings {
		if entry.Name == newName {
			return fmt.Errorf("%w: %s", vfs.ErrExists, newPath)
		}
	}
	entry := children[idx]
	// Build a fresh slice rather than append(children[:idx], ...), which would
	// clobber the backing array shared with any listing already handed out.
	remaining := make([]domain.Entry, 0, len(children)-1)
	remaining = append(remaining, children[:idx]...)
	remaining = append(remaining, children[idx+1:]...)
	r.entries[oldParent] = remaining
	entry.Name = newName
	r.entries[newParent] = append(r.entries[newParent], entry)
	if entry.IsDir() {
		moved := map[string][]domain.Entry{}
		for dirPath, entries := range r.entries {
			if dirPath == oldPath || strings.HasPrefix(dirPath, oldPath+"/") {
				moved[newPath+strings.TrimPrefix(dirPath, oldPath)] = entries
				delete(r.entries, dirPath)
			}
		}
		for dirPath, entries := range moved {
			r.entries[dirPath] = entries
		}
	}
	return nil
}

func (r *Remote) Remove(ctx context.Context, targetPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	targetPath = clean(targetPath)
	parent, name := path.Dir(targetPath), path.Base(targetPath)
	children, ok := r.entries[parent]
	if !ok {
		return fmt.Errorf("no such directory: %s", parent)
	}
	idx := -1
	for i, entry := range children {
		if entry.Name == name {
			if entry.IsDir() && len(r.entries[targetPath]) > 0 {
				return fmt.Errorf("directory not empty: %s", targetPath)
			}
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("no such item: %s", targetPath)
	}
	remaining := make([]domain.Entry, 0, len(children)-1)
	remaining = append(remaining, children[:idx]...)
	remaining = append(remaining, children[idx+1:]...)
	r.entries[parent] = remaining
	delete(r.entries, targetPath)
	return nil
}

func (r *Remote) ReadFile(ctx context.Context, filePath string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	filePath = clean(filePath)
	if !r.isFile(filePath) {
		return nil, fmt.Errorf("no such file: %s", filePath)
	}
	body := r.contents[filePath]
	out := make([]byte, len(body))
	copy(out, body)
	return out, nil
}

func (r *Remote) WriteFile(ctx context.Context, filePath string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	filePath = clean(filePath)
	parent, name := path.Dir(filePath), path.Base(filePath)
	children, ok := r.entries[parent]
	if !ok {
		return fmt.Errorf("no such directory: %s", parent)
	}
	if _, isDir := r.entries[filePath]; isDir {
		return fmt.Errorf("is a directory: %s", filePath)
	}
	stored := make([]byte, len(data))
	copy(stored, data)
	if r.contents == nil {
		r.contents = map[string][]byte{}
	}
	r.contents[filePath] = stored
	if !r.isFile(filePath) {
		r.entries[parent] = append(children, file(name, int64(len(stored)), time.Now()))
	}
	return nil
}

// isFile reports whether filePath is listed as a non-directory entry in its
// parent.
func (r *Remote) isFile(filePath string) bool {
	parent, name := path.Dir(filePath), path.Base(filePath)
	for _, entry := range r.entries[parent] {
		if entry.Name == name {
			return !entry.IsDir()
		}
	}
	return false
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
