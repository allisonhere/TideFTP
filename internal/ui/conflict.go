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
// the policy row cursor, enter applies it to this batch only, s applies it
// and remembers it for the rest of the session, esc/q/n cancels the whole
// batch — including any files in it that had no conflict at all, the same
// all-or-nothing cancel every other confirm overlay already has.
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
		return m.confirmConflict(false)
	case "s":
		return m.confirmConflict(true)
	}
	return nil
}

// confirmConflict applies the policy under the overlay's cursor to the
// pending scan and queues the result. remember additionally sets
// Model.sessionConflictPolicy, so a later conflicting batch resolves the
// same way without asking again.
func (m *Model) confirmConflict(remember bool) tea.Cmd {
	if m.preflight == nil {
		return nil
	}
	scan := *m.preflight
	m.preflight = nil
	m.overlay = overlayNone

	policy := conflictPolicy(scan.cursor)
	if remember {
		m.sessionConflictPolicy = &policy
	}
	m.commitScan(scan, &policy)
	return nil
}

// commitScan turns a resolved preflightScan into queued domain.Transfers and
// starts the queue. policy is nil when scan has no conflicts at all — every
// file queues unconditionally in that case. Otherwise every conflicting
// file is resolved per *policy; a clean file (no conflict) always queues
// regardless of policy.
func (m *Model) commitScan(scan preflightScan, policy *conflictPolicy) {
	claimed := map[string]bool{}
	queued := 0
	for _, f := range scan.files {
		dst, resumeFrom := f.dst, int64(0)
		if f.conflict != nil {
			switch *policy {
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
