package ui

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
	"tideftp/internal/vfs"
)

// syncNewerSkew is how much newer a source file's mtime must be before a
// same-size file counts as changed. It absorbs clock skew between client and
// server and filesystems that store mtimes at coarse resolution.
const syncNewerSkew = 2 * time.Second

// prunePath is one entry present at the destination but not the source,
// queued for deletion when the mirror runs with prune armed. Directories are
// listed after everything inside them so a bottom-up delete finds them
// empty.
type prunePath struct {
	path  string
	name  string
	isDir bool
}

// syncPlan is the outcome of walking both trees for a directory mirror,
// held in Model.sync and shown as overlaySync before anything is queued or
// deleted. copies carries the files to transfer (each already shaped for
// commitScan — an existing destination file gets a conflict + an
// unconditional overwrite resolution); prunePaths carries the extras, only
// acted on when prune is armed.
type syncPlan struct {
	direction domain.TransferDirection
	srcRoot   string
	dstRoot   string
	dstFS     vfs.FS

	copies     []preflightFile
	updates    int // how many of copies replace an existing destination file
	identical  int
	totalBytes int64

	prunePaths []prunePath
	pruneBytes int64
	prune      bool // armed by the overlay's "p"

	truncated bool // hit preflightScanCap; counts are a lower bound
}

func (p syncPlan) newCount() int    { return len(p.copies) - p.updates }
func (p syncPlan) updateCount() int { return p.updates }

func (p syncPlan) pruneFileCount() int {
	n := 0
	for _, e := range p.prunePaths {
		if !e.isDir {
			n++
		}
	}
	return n
}

func (p syncPlan) pruneDirCount() int {
	n := 0
	for _, e := range p.prunePaths {
		if e.isDir {
			n++
		}
	}
	return n
}

// empty reports whether the plan has nothing to do at all — trees already
// match and there is not even an extra to prune.
func (p syncPlan) empty() bool {
	return len(p.copies) == 0 && len(p.prunePaths) == 0
}

// count is how many entries the walk has accumulated so far, for the cap.
func (p syncPlan) count() int { return len(p.copies) + p.identical + len(p.prunePaths) }

// syncNeedsCopy compares a source file against its destination counterpart.
// A missing destination is a copy; a different size, or a source mtime
// meaningfully newer than the destination's, is an update; everything else
// is left alone.
func syncNeedsCopy(src, dst domain.Entry, dstExists bool) (need, isUpdate bool) {
	if !dstExists {
		return true, false
	}
	if src.Size != dst.Size {
		return true, true
	}
	if !src.Modified.IsZero() && !dst.Modified.IsZero() && src.Modified.After(dst.Modified.Add(syncNewerSkew)) {
		return true, true
	}
	return false, false
}

// syncScanMsg reports beginSyncScan's result.
type syncScanMsg struct {
	plan syncPlan
	err  error
}

// startSync kicks off a directory mirror from the focused pane to the other.
// A directory under the cursor scopes it to that subtree; otherwise the
// whole pane is mirrored. It needs a live connection — both trees have to be
// walked.
func (m *Model) startSync() tea.Cmd {
	if !m.connected() {
		m.setError("not connected")
		return nil
	}
	paneID, ok := m.focusedPaneID()
	if !ok {
		m.setError("focus a file pane to mirror from")
		return nil
	}
	src := m.filePaneByID(paneID)

	var direction domain.TransferDirection
	var srcFS, dstFS vfs.FS
	var srcBase, dstBase string
	if paneID == paneLocal {
		direction, srcFS, dstFS = domain.Upload, m.localFS, m.remoteFS
		srcBase, dstBase = m.local.path, m.remote.path
	} else {
		direction, srcFS, dstFS = domain.Download, m.remoteFS, m.localFS
		srcBase, dstBase = m.remote.path, m.local.path
	}
	if entry, found := src.current(); found && entry.IsDir() && !isParentDirEntry(entry) {
		srcBase = srcFS.Child(srcBase, entry.Name)
		dstBase = dstFS.Child(dstBase, entry.Name)
	}
	m.setStatus("scanning for changes…")
	return m.beginSyncScan(direction, srcBase, dstBase, srcFS, dstFS, src.showHidden)
}

// beginSyncScan walks the source and destination trees in step, off the UI
// goroutine, and delivers a syncPlan. Hidden entries are in or out of the
// whole operation together, following the source pane's toggle: a hidden
// file is neither copied nor counted as an extra to prune when hidden files
// are off.
func (m *Model) beginSyncScan(direction domain.TransferDirection, srcBase, dstBase string, srcFS, dstFS vfs.FS, showHidden bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), preflightScanTimeout)
		defer cancel()

		plan := syncPlan{
			direction: direction,
			srcRoot:   srcBase,
			dstRoot:   dstBase,
			dstFS:     dstFS,
		}

		type dirPair struct{ srcDir, dstDir string }
		stack := []dirPair{{srcBase, dstBase}}
		var orphanDirs []string

		for len(stack) > 0 {
			if plan.count() >= preflightScanCap {
				plan.truncated = true
				break
			}
			it := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			srcChildren, err := srcFS.List(ctx, it.srcDir, showHidden)
			if err != nil {
				return syncScanMsg{err: fmt.Errorf("read %s: %w", it.srcDir, err)}
			}
			// A destination directory that will not list is almost always one
			// that does not exist yet — a subtree the mirror is about to
			// create. That is not a scan failure; it just means nothing there
			// matches and nothing there is an extra.
			dstChildren, _ := dstFS.List(ctx, it.dstDir, showHidden)
			dstByName := make(map[string]domain.Entry, len(dstChildren))
			for _, e := range dstChildren {
				dstByName[e.Name] = e
			}
			srcNames := make(map[string]bool, len(srcChildren))

			for _, child := range srcChildren {
				srcNames[child.Name] = true
				childSrc := srcFS.Child(it.srcDir, child.Name)
				childDst := dstFS.Child(it.dstDir, child.Name)
				if child.IsDir() {
					stack = append(stack, dirPair{childSrc, childDst})
					continue
				}
				if plan.count() >= preflightScanCap {
					plan.truncated = true
					break
				}
				dstEntry, exists := dstByName[child.Name]
				need, isUpdate := syncNeedsCopy(child, dstEntry, exists)
				if !need {
					plan.identical++
					continue
				}
				pf := preflightFile{src: childSrc, dst: childDst, name: child.Name, size: child.Size, modified: child.Modified}
				if isUpdate {
					e := dstEntry
					pf.conflict = &e
					res := conflictOverwrite
					pf.resolution = &res
					plan.updates++
				}
				plan.copies = append(plan.copies, pf)
				plan.totalBytes += child.Size
			}

			for _, child := range dstChildren {
				if srcNames[child.Name] {
					continue
				}
				childDst := dstFS.Child(it.dstDir, child.Name)
				if child.IsDir() {
					orphanDirs = append(orphanDirs, childDst)
					continue
				}
				plan.prunePaths = append(plan.prunePaths, prunePath{path: childDst, name: child.Name})
				plan.pruneBytes += child.Size
			}
		}

		// Every extra destination subtree, flattened so prune can delete it
		// bottom-up.
		for _, root := range orphanDirs {
			collectPruneTree(ctx, dstFS, root, showHidden, &plan)
		}
		sort.SliceStable(plan.prunePaths, func(i, j int) bool {
			return strings.Count(plan.prunePaths[i].path, "/") > strings.Count(plan.prunePaths[j].path, "/")
		})

		return syncScanMsg{plan: plan}
	}
}

// collectPruneTree adds root and everything under it to plan.prunePaths, so a
// destination subtree with no source counterpart can be removed entirely.
func collectPruneTree(ctx context.Context, dstFS vfs.FS, root string, showHidden bool, plan *syncPlan) {
	stack := []string{root}
	for len(stack) > 0 {
		if plan.count() >= preflightScanCap {
			plan.truncated = true
			return
		}
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		plan.prunePaths = append(plan.prunePaths, prunePath{path: dir, name: path.Base(dir), isDir: true})
		children, err := dstFS.List(ctx, dir, showHidden)
		if err != nil {
			continue
		}
		for _, child := range children {
			childPath := dstFS.Child(dir, child.Name)
			if child.IsDir() {
				stack = append(stack, childPath)
				continue
			}
			plan.prunePaths = append(plan.prunePaths, prunePath{path: childPath, name: child.Name})
			plan.pruneBytes += child.Size
		}
	}
}

// applySyncScan folds a completed scan into the model: an error is reported,
// a plan with nothing to do says so, and anything else opens overlaySync for
// confirmation.
func (m *Model) applySyncScan(msg syncScanMsg) {
	if msg.err != nil {
		m.setError(fmt.Sprintf("mirror scan: %v", msg.err))
		return
	}
	if msg.plan.empty() {
		m.setStatus("already in sync — nothing to mirror")
		return
	}
	plan := msg.plan
	m.sync = &plan
	m.overlay = overlaySync
	m.setStatus("review the mirror plan")
}

// handleSyncKey routes keys while overlaySync is open: p arms or disarms the
// prune, enter runs the mirror, esc cancels the whole thing.
func (m *Model) handleSyncKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q", "n":
		m.sync = nil
		m.overlay = overlayNone
		m.setStatus("mirror cancelled")
	case "p":
		if m.sync != nil {
			m.sync.prune = !m.sync.prune
			if m.sync.prune {
				m.setStatus("prune armed — extras will be deleted")
			} else {
				m.setStatus("prune off — extras left alone")
			}
		}
	case "enter", "y":
		return m.confirmSync()
	}
	return nil
}

// confirmSync commits the plan: the copies and updates go through the same
// commitScan path an ordinary folder queue uses, and, if prune is armed, the
// extras are deleted by syncPruneCmd.
func (m *Model) confirmSync() tea.Cmd {
	if m.sync == nil {
		return nil
	}
	plan := m.sync
	m.sync = nil
	m.overlay = overlayNone

	if len(plan.copies) > 0 {
		m.commitScan(preflightScan{
			direction: plan.direction,
			files:     plan.copies,
			dstFS:     plan.dstFS,
		})
	} else {
		m.setStatus("nothing to transfer")
	}
	if plan.prune && len(plan.prunePaths) > 0 {
		return syncPruneCmd(plan.dstFS, plan.prunePaths)
	}
	return nil
}

// syncPruneMsg reports syncPruneCmd's result.
type syncPruneMsg struct {
	removed int
	failed  int
	err     error
}

// syncPruneCmd deletes every path in the list, in the order it was given
// (deepest first, so a directory is empty by the time its turn comes). A
// failure on one entry is recorded and the rest are still attempted — a
// permission problem on a single file should not strand the remaining
// deletes.
func syncPruneCmd(dstFS vfs.FS, paths []prunePath) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), preflightScanTimeout)
		defer cancel()
		var msg syncPruneMsg
		for _, p := range paths {
			if err := dstFS.Remove(ctx, p.path); err != nil {
				msg.failed++
				if msg.err == nil {
					msg.err = err
				}
				continue
			}
			msg.removed++
		}
		return msg
	}
}

// syncDirectionLabel names the mirror direction for the overlay header.
func syncDirectionLabel(direction domain.TransferDirection) string {
	if direction == domain.Download {
		return "remote → local"
	}
	return "local → remote"
}
