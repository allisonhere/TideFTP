package ui

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
	"tideftp/internal/vfs"
)

// autoResumeVerifyLimit bounds how much existing data TideFTP reads back to
// prove a partial destination is the source prefix. Larger partials remain in
// Failed for an explicit retry rather than treating a matching size as proof.
const autoResumeVerifyLimit int64 = 16 << 20

// recoveryRetry is an interrupted transfer whose destination was checked
// after reconnecting and is safe to start again, either from byte zero or an
// existing partial size.
type recoveryRetry struct {
	originalID  int
	direction   domain.TransferDirection
	source      string
	destination string
	size        int64
	offset      int64
	protocol    string
}

// recoveryScanMsg is the categorized result of checking every interrupted
// transfer after a reconnect. partial and missing are safe to start without a
// decision; mismatched and unreachable are the exceptions the review panel
// exists to surface.
type recoveryScanMsg struct {
	token       int
	partial     []recoveryRetry // verified partial destination: resume from offset
	missing     []recoveryRetry // no destination yet: restart from zero
	mismatched  map[int]string  // not safely usable: the user chooses restart or skip
	unreachable map[int]string  // destination could not be inspected: leave failed
}

// recoverySummary is the pending post-reconnect decision while
// overlayRecovery is open. It is cleared once the user resolves it.
type recoverySummary struct {
	partial     []recoveryRetry
	missing     []recoveryRetry
	mismatched  map[int]string
	unreachable map[int]string
}

// safe is how many transfers can be resumed with no per-file decision: the
// verified partials plus the ones whose destination simply is not there yet.
func (s recoverySummary) safe() int {
	return len(s.partial) + len(s.missing)
}

// recoveryChoice is which of the panel's bulk actions the user picked.
type recoveryChoice int

const (
	recoveryResumeSafe recoveryChoice = iota
	recoveryRestartRemaining
	recoverySkipRemaining
)

func (m *Model) startInterruptedRecovery() tea.Cmd {
	candidates := make([]domain.Transfer, 0)
	for _, row := range m.transfers {
		if row.RetryOnReconnect {
			candidates = append(candidates, row)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	ctx, token, tick := m.startScan("Restoring interrupted transfers", "Checking partial destinations")
	events := make(chan tea.Msg, 1)
	m.scanEvents = events
	go func() {
		defer close(events)
		report := func(progress scanProgressMsg) {
			select {
			case events <- progress:
			default:
			}
		}
		events <- runInterruptedRecovery(ctx, token, candidates, m.localFS, m.remoteFS, report)
	}()
	return tea.Batch(waitForScanEvent(events), tick)
}

func runInterruptedRecovery(ctx context.Context, token int, candidates []domain.Transfer, local, remote vfs.FS, report func(scanProgressMsg)) recoveryScanMsg {
	result := recoveryScanMsg{
		token:       token,
		mismatched:  make(map[int]string),
		unreachable: make(map[int]string),
	}
	byDir := make(map[string][]domain.Transfer)
	fsByDir := make(map[string]vfs.FS)
	for _, row := range candidates {
		fs := remote
		if row.Direction == domain.Download {
			fs = local
		}
		if fs == nil {
			result.unreachable[row.ID] = "destination unavailable; retry with R"
			continue
		}
		dir := fs.Parent(row.Destination)
		key := fmt.Sprintf("%d:%s", row.Direction, dir)
		byDir[key] = append(byDir[key], row)
		fsByDir[key] = fs
	}

	checked := 0
	for key, rows := range byDir {
		fs := fsByDir[key]
		dir := fs.Parent(rows[0].Destination)
		listed, err := fs.List(ctx, dir, true)
		checked += len(rows)
		if report != nil {
			report(scanProgressMsg{token: token, phase: "Checking partial destinations", files: checked, bytes: totalRecoveryBytes(candidates), current: dir})
		}
		if err != nil {
			for _, row := range rows {
				result.unreachable[row.ID] = "could not inspect destination; retry with R"
			}
			continue
		}
		for _, row := range rows {
			name := path.Base(row.Destination)
			var hit *domain.Entry
			for _, entry := range listed {
				if entry.Name == name {
					copy := entry
					hit = &copy
					break
				}
			}
			if hit != nil && hit.IsDir() {
				result.mismatched[row.ID] = "destination is a folder; retry with R"
				continue
			}
			if hit != nil && row.BytesTotal > 0 && hit.Size >= row.BytesTotal {
				result.mismatched[row.ID] = "destination is already full; review before retrying"
				continue
			}
			if hit == nil {
				result.missing = append(result.missing, recoveryRetry{originalID: row.ID, direction: row.Direction, source: row.Source, destination: row.Destination, size: row.BytesTotal, protocol: row.Protocol})
				continue
			}
			if hit.Size > autoResumeVerifyLimit {
				result.mismatched[row.ID] = "partial is too large to verify automatically; retry with R"
				continue
			}
			srcFS, dstFS := local, remote
			if row.Direction == domain.Download {
				srcFS, dstFS = remote, local
			}
			if err := matchingPrefix(ctx, srcFS, row.Source, dstFS, row.Destination, hit.Size); err != nil {
				result.mismatched[row.ID] = "partial could not be verified; retry with R"
				continue
			}
			result.partial = append(result.partial, recoveryRetry{originalID: row.ID, direction: row.Direction, source: row.Source, destination: row.Destination, size: row.BytesTotal, offset: hit.Size, protocol: row.Protocol})
		}
	}
	return result
}

func matchingPrefix(ctx context.Context, srcFS vfs.FS, srcPath string, dstFS vfs.FS, dstPath string, size int64) error {
	if size <= 0 {
		return nil
	}
	src, err := srcFS.Open(ctx, srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := dstFS.Open(ctx, dstPath)
	if err != nil {
		return err
	}
	defer dst.Close()
	left, right := make([]byte, 64*1024), make([]byte, 64*1024)
	for remaining := size; remaining > 0; {
		want := int(min(remaining, int64(len(left))))
		if _, err := io.ReadFull(src, left[:want]); err != nil {
			return err
		}
		if _, err := io.ReadFull(dst, right[:want]); err != nil {
			return err
		}
		for i := 0; i < want; i++ {
			if left[i] != right[i] {
				return fmt.Errorf("partial contents differ")
			}
		}
		remaining -= int64(want)
	}
	return nil
}

func totalRecoveryBytes(rows []domain.Transfer) int64 {
	var total int64
	for _, row := range rows {
		total += row.BytesTotal
	}
	return total
}

// applyInterruptedRecovery records the check result and opens the review
// panel. Nothing is queued here: the safe transfers wait for the user's
// enter, so a reconnect cannot silently put a large batch back on the wire
// before the exceptions have been seen.
func (m *Model) applyInterruptedRecovery(msg recoveryScanMsg) {
	for i := range m.transfers {
		if !m.transfers[i].RetryOnReconnect {
			continue
		}
		m.transfers[i].RetryOnReconnect = false
		if reason, bad := msg.mismatched[m.transfers[i].ID]; bad {
			m.transfers[i].Message = "interrupted — " + reason
		}
		if reason, bad := msg.unreachable[m.transfers[i].ID]; bad {
			m.transfers[i].Message = "interrupted — " + reason
		}
	}
	summary := &recoverySummary{partial: msg.partial, missing: msg.missing, mismatched: msg.mismatched, unreachable: msg.unreachable}
	m.recoverySummary = summary
	m.overlay = overlayRecovery
	m.setStatus(fmt.Sprintf("reconnected — %d interrupted transfer(s) to review", summary.safe()+len(summary.mismatched)+len(summary.unreachable)))
}

// handleRecoveryKey drives the review panel. enter/r/s each resolve it in one
// step: all three resume the safe set, and they differ only in what happens
// to the mismatched transfers the user has to decide on. Unreachable ones are
// never bulk-restarted; they stay in Failed for an individual retry.
func (m *Model) handleRecoveryKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		return m.resolveRecovery(recoveryResumeSafe)
	case "r":
		return m.resolveRecovery(recoveryRestartRemaining)
	case "s":
		return m.resolveRecovery(recoverySkipRemaining)
	case "down", "j":
		return m.inspectRecoveryExceptions()
	case "esc", "q":
		m.recoverySummary = nil
		m.overlay = overlayNone
		m.setStatus("interrupted transfers left in Failed")
	}
	return nil
}

func (m *Model) resolveRecovery(choice recoveryChoice) tea.Cmd {
	summary := m.recoverySummary
	if summary == nil {
		m.overlay = overlayNone
		return nil
	}
	if !m.connected() {
		m.setError("reconnect before resuming interrupted transfers")
		return nil
	}

	restarting := 0
	if choice == recoveryRestartRemaining {
		restarting = len(summary.mismatched)
	}
	if queued := summary.safe() + restarting; queued > 0 && !m.queueBusy() {
		m.queueBatchStartID = m.nextTransferID
		m.queueBatchActive = true
	}

	resumed := m.queueRecoveryRetries(summary.partial, "resuming after reconnect")
	resumed += m.queueRecoveryRetries(summary.missing, "restarting after reconnect")
	restarted := 0
	if choice == recoveryRestartRemaining {
		restarted = m.restartRecoveryExceptions(summary.mismatched)
	}

	m.recoverySummary = nil
	m.overlay = overlayNone
	switch choice {
	case recoveryRestartRemaining:
		m.setStatus(fmt.Sprintf("reconnected — resuming %d, restarting %d from zero", resumed, restarted))
	case recoverySkipRemaining:
		m.setStatus(fmt.Sprintf("reconnected — resuming %d, skipped %d", resumed, len(summary.mismatched)))
	default:
		m.setStatus(fmt.Sprintf("reconnected — resuming %d interrupted transfer(s)", resumed))
	}
	if resumed+restarted > 0 {
		m.startQueuedTransfers()
	}
	return nil
}

// queueRecoveryRetries appends a fresh queued transfer for each checked retry
// and marks the original Failed row as resumed, so the record of what was
// interrupted stays visible next to the attempt that replaced it.
func (m *Model) queueRecoveryRetries(retries []recoveryRetry, message string) int {
	for _, retry := range retries {
		id := m.nextTransferID
		m.transfers = append(m.transfers, domain.Transfer{
			ID: id, Direction: retry.direction, Source: retry.source, Destination: retry.destination,
			BytesTotal: retry.size, BytesDone: retry.offset, ResumeFrom: retry.offset,
			Status: domain.Queued, Message: message, Protocol: retry.protocol,
		})
		m.nextTransferID++
		if index := m.transferIndex(retry.originalID); index >= 0 {
			m.transfers[index].Message = fmt.Sprintf("interrupted — resumed as %d", id)
		}
	}
	return len(retries)
}

// restartRecoveryExceptions queues the mismatched transfers again from zero.
// A single keypress is enough because the panel is already the confirmation;
// unlike the per-row R, it is not a two-press arm.
func (m *Model) restartRecoveryExceptions(manual map[int]string) int {
	count := 0
	for id := range manual {
		index := m.transferIndex(id)
		if index < 0 {
			continue
		}
		original := &m.transfers[index]
		original.Message = fmt.Sprintf("interrupted — restarted as %d", m.nextTransferID)
		m.transfers = append(m.transfers, domain.Transfer{
			ID: m.nextTransferID, Direction: original.Direction, Source: original.Source, Destination: original.Destination,
			BytesTotal: original.BytesTotal, Status: domain.Queued, Message: "restarted after reconnect", Protocol: original.Protocol,
		})
		m.nextTransferID++
		count++
	}
	return count
}

// inspectRecoveryExceptions closes the panel and lands the cursor on the
// Failed tab so the user can retry one exception at a time instead of taking
// a bulk action.
func (m *Model) inspectRecoveryExceptions() tea.Cmd {
	m.recoverySummary = nil
	m.overlay = overlayNone
	m.focus = focusQueue
	m.setBottomTab(tabFailed)
	m.reachFailedTransfer()
	m.setStatus("interrupted transfers in Failed — R retries the selected row")
	return nil
}

// renderRecoveryOverlay is the categorized review panel: what recovery found
// and the keys that dispose of it. It exists because a reconnect can leave a
// large batch in a mixed state, and one modal that explains the whole shape
// beats a prompt per file.
func (m Model) renderRecoveryOverlay(renderer tideui.Renderer) *tideui.Overlay {
	summary := m.recoverySummary
	if summary == nil {
		return nil
	}
	width := min(64, max(44, m.width-8))
	contentWidth := width - 4
	rows := []string{
		renderer.RenderSoftRow(tideui.SoftRow{Text: fmt.Sprintf("%d verified partial(s)", len(summary.partial)), Suffix: "safe to resume", Muted: len(summary.partial) == 0}, contentWidth),
		renderer.RenderSoftRow(tideui.SoftRow{Text: fmt.Sprintf("%d destination(s) missing", len(summary.missing)), Suffix: "safe to restart", Muted: len(summary.missing) == 0}, contentWidth),
		renderer.RenderSoftRow(tideui.SoftRow{Text: fmt.Sprintf("%d mismatched/full", len(summary.mismatched)), Suffix: "need a decision", Muted: len(summary.mismatched) == 0}, contentWidth),
		renderer.RenderSoftRow(tideui.SoftRow{Text: fmt.Sprintf("%d unreachable", len(summary.unreachable)), Suffix: "leave failed", Muted: len(summary.unreachable) == 0}, contentWidth),
		"",
		renderer.RenderSoftHints(contentWidth, tideui.SoftHint{Key: "enter", Label: fmt.Sprintf("resume safe (%d)", summary.safe())}),
		renderer.RenderSoftHints(contentWidth, tideui.SoftHint{Key: "r", Label: "restart remaining"}),
		renderer.RenderSoftHints(contentWidth, tideui.SoftHint{Key: "s", Label: "skip remaining"}),
		renderer.RenderSoftHints(contentWidth, tideui.SoftHint{Key: "↓", Label: "inspect individual exceptions"}),
		renderer.RenderSoftHints(contentWidth, tideui.SoftHint{Key: "esc", Label: "decide later"}),
	}
	overlay := renderer.SoftPanelOverlay(tideui.SoftPanel{Prefix: "tideftp", Title: "interrupted transfers", Width: width, Content: renderer.RenderSoftBody(width, strings.Join(rows, "\n"))})
	return &overlay
}
