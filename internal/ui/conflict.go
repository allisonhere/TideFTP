package ui

import (
	"fmt"
	"path"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
	"tideftp/internal/vfs"
)

// conflictPolicy names one way to resolve a file that already exists at its
// destination, mirroring the FileZilla-style options from docs/handoff.md's
// Product Decisions.
type conflictPolicy int

const (
	conflictOverwrite conflictPolicy = iota
	conflictOverwriteIfSourceNewer
	conflictOverwriteIfDifferentSize
	conflictOverwriteIfDifferentSizeOrNewer
	conflictResume
	conflictRename
	conflictSkip
	conflictPolicyCount
)

func conflictPolicyLabel(policy conflictPolicy) string {
	switch policy {
	case conflictOverwrite:
		return "Overwrite"
	case conflictOverwriteIfSourceNewer:
		return "Overwrite if source newer"
	case conflictOverwriteIfDifferentSize:
		return "Overwrite if different size"
	case conflictOverwriteIfDifferentSizeOrNewer:
		return "Overwrite if different size or source newer"
	case conflictResume:
		return "Resume"
	case conflictRename:
		return "Rename"
	case conflictSkip:
		return "Skip"
	}
	return ""
}

// handleConflictKey routes keys while overlayConflict is open: up/down move
// the policy row cursor, enter applies it to just the one conflict currently
// shown and advances to the next (or commits if that was the last one), a
// applies it to every remaining conflict in the batch at once, s does that
// and also remembers it for the rest of the session, esc/q/n cancels the
// whole batch — including any files in it that had no conflict at all, the
// same all-or-nothing cancel every other confirm overlay already has.
func (m *Model) handleConflictKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q", "n":
		m.preflight = nil
		m.overlay = overlayNone
		m.setStatus("cancelled")
	case "up", "k":
		if m.preflight != nil {
			m.preflight.moveCursor(-1)
		}
	case "down", "j":
		if m.preflight != nil {
			m.preflight.moveCursor(1)
		}
	case "enter", "y":
		return m.resolveOneConflict()
	case "a":
		return m.resolveAllConflicts(false)
	case "s":
		return m.resolveAllConflicts(true)
	}
	return nil
}

// resolveOneConflict applies the overlay cursor's policy to just the
// conflict currently shown. If another conflict remains in the batch, the
// overlay stays open on it; once every conflict has a resolution, the whole
// batch — resolved conflicts and clean files alike — queues.
func (m *Model) resolveOneConflict() tea.Cmd {
	if m.preflight == nil {
		return nil
	}
	policy := conflictPolicy(m.preflight.cursor)
	if m.preflight.resolveCurrent(policy) {
		return nil // another conflict remains; stay open on it
	}
	scan := *m.preflight
	m.preflight = nil
	m.overlay = overlayNone
	m.commitScan(scan)
	return nil
}

// resolveAllConflicts applies the overlay cursor's policy to every
// remaining unresolved conflict in the batch at once and queues
// immediately. remember additionally sets Model.sessionConflictPolicy, so a
// later conflicting batch resolves the same way without asking again.
func (m *Model) resolveAllConflicts(remember bool) tea.Cmd {
	if m.preflight == nil {
		return nil
	}
	policy := conflictPolicy(m.preflight.cursor)
	m.preflight.resolveAllRemaining(policy)
	if remember {
		m.sessionConflictPolicy = &policy
	}
	scan := *m.preflight
	m.preflight = nil
	m.overlay = overlayNone
	m.commitScan(scan)
	return nil
}

// commitScan turns a resolved preflightScan into queued domain.Transfers and
// starts the queue. Every conflicting file must already have a resolution
// by the time this runs (resolveOneConflict/resolveAllConflicts guarantee
// it); a clean file (no conflict) always queues unconditionally.
func (m *Model) commitScan(scan preflightScan) {
	claimed := map[string]bool{}
	queued := 0
	for _, f := range scan.files {
		dst, resumeFrom := f.dst, int64(0)
		if f.conflict != nil {
			switch *f.resolution {
			case conflictOverwrite:
				// dst and resumeFrom already right: overwrite unconditionally.
			case conflictOverwriteIfSourceNewer:
				if !f.modified.After(f.conflict.Modified) {
					continue
				}
			case conflictOverwriteIfDifferentSize:
				if f.size == f.conflict.Size {
					continue
				}
			case conflictOverwriteIfDifferentSizeOrNewer:
				if f.size == f.conflict.Size && !f.modified.After(f.conflict.Modified) {
					continue
				}
			case conflictResume:
				if f.conflict.Size >= f.size {
					continue // nothing left to resume; treat as already complete
				}
				resumeFrom = f.conflict.Size
			case conflictRename:
				dir := scan.dstFS.Parent(f.dst)
				dst = renameDestination(scan.dstFS, dir, f.name, scan.siblings[dir], claimed)
				claimed[dst] = true
			case conflictSkip:
				continue
			}
		}
		m.transfers = append(m.transfers, domain.Transfer{
			ID:          m.nextTransferID,
			Direction:   scan.direction,
			Source:      f.src,
			Destination: dst,
			BytesTotal:  f.size,
			BytesDone:   resumeFrom,
			ResumeFrom:  resumeFrom,
			Status:      domain.Queued,
			Message:     "queued",
			Protocol:    m.target.Protocol,
		})
		m.nextTransferID++
		queued++
	}
	if scan.folders > 0 {
		m.setStatus(fmt.Sprintf("queued %d transfer(s) from %d folder(s)", queued, scan.folders))
	} else {
		m.setStatus(fmt.Sprintf("queued %d transfer(s)", queued))
	}
	m.startQueuedTransfers()
}

// renameDestinationCap bounds how many numbered suffixes renameDestination
// will try before giving up, so a pathological run of pre-existing
// "name (N)" siblings cannot loop forever.
const renameDestinationCap = 10000

// renameDestination finds a destination path for name inside dir that
// collides with neither an existing sibling nor a name already claimed
// earlier in the same batch, by trying "stem (1)ext", "stem (2)ext", and so
// on. name is a bare leaf filename, so splitting its extension with path.Ext
// is safe regardless of whether dir is a local or remote path.
func renameDestination(fs vfs.FS, dir, name string, siblings map[string]domain.Entry, claimed map[string]bool) string {
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; i <= renameDestinationCap; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		full := fs.Child(dir, candidate)
		if _, exists := siblings[candidate]; exists {
			continue
		}
		if claimed[full] {
			continue
		}
		return full
	}
	// Every reasonable candidate collided; fall back to the original name
	// rather than looping forever. This overwrites, same as conflictOverwrite
	// would — an acceptable last resort for a case this pathological.
	return fs.Child(dir, name)
}
