package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"tideftp/internal/domain"
	"tideftp/internal/vfs"
)

const (
	// deleteScanCap bounds the count walk that runs before the confirmation
	// prompt. It is deliberately looser than preflightScanCap: that cap
	// decides how much work actually gets queued, so stopping early is a real
	// limit, while this one only decides how precise the prompt and the
	// progress denominator are. Running past it costs the user a "20000+"
	// instead of an exact number, nothing more.
	deleteScanCap = 20000
	// deleteScanBudget bounds the whole count walk. Overrunning it is not an
	// error — the scan degrades to a truncated count — because a slow count
	// that reported failure would reintroduce the dead-looking wait this
	// feature exists to remove, one phase earlier.
	deleteScanBudget = 20 * time.Second
	// deleteStepTimeout bounds one List or one Remove. The walk as a whole
	// carries no deadline on purpose: a tree big enough to take an hour is
	// exactly the case this job is for, and the single 60s budget that used
	// to cover the entire recursive delete is what left folders half deleted
	// with "context deadline exceeded" against them.
	deleteStepTimeout = 30 * time.Second
	// deleteFlushInterval and deleteFlushCount coalesce removals into one
	// message. A local delete of 50000 files would otherwise push 50000
	// messages through the event loop, each one a full re-render.
	deleteFlushInterval = 100 * time.Millisecond
	deleteFlushCount    = 64
	// deleteTickInterval advances the spinner. It runs on a timer rather than
	// on events because a stalled server would otherwise freeze the spinner,
	// and a frozen spinner reads as exactly the hang this is here to dispel.
	deleteTickInterval = 120 * time.Millisecond
	// deleteRowHeight is how many rows the pinned progress block occupies. It
	// is a constant because the bottom pane's scroll arithmetic
	// (bottomVisibleRows, clampBottomCursor) has to agree with the renderer
	// about how much room the block takes.
	deleteRowHeight = 2
)

// deleteScan is the result of counting a delete selection before asking the
// user to confirm it. It is a count and not a plan: the job re-walks the tree
// itself, so a truncated scan costs precision in the prompt and in the
// progress denominator and cannot cost correctness.
type deleteScan struct {
	files     int
	folders   int
	bytes     int64
	truncated bool
}

func (s deleteScan) total() int { return s.files + s.folders }

// deleteScanMsg carries a finished count back to the prompt that asked for it.
type deleteScanMsg struct {
	pane    paneID
	entries []domain.Entry
	scan    deleteScan
}

// beginDeleteScan counts everything a delete of entries would remove, walking
// iteratively over a stack the way collectPruneTree does. A directory that
// will not list is skipped rather than failed: the delete is going to attempt
// it anyway, and that is where the error belongs.
func beginDeleteScan(fs vfs.FS, base string, pane paneID, entries []domain.Entry) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), deleteScanBudget)
		defer cancel()

		msg := deleteScanMsg{pane: pane, entries: entries}
		var stack []string
		for _, entry := range entries {
			if isParentDirEntry(entry) {
				continue
			}
			if entry.IsDir() {
				msg.scan.folders++
				stack = append(stack, fs.Child(base, entry.Name))
				continue
			}
			msg.scan.files++
			msg.scan.bytes += entry.Size
		}

		for len(stack) > 0 {
			if msg.scan.total() >= deleteScanCap || ctx.Err() != nil {
				msg.scan.truncated = true
				return msg
			}
			dir := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			children, err := fs.List(ctx, dir, true)
			if err != nil {
				continue
			}
			for _, child := range children {
				if isParentDirEntry(child) {
					continue
				}
				// Checked here as well as at the top of the walk: one
				// directory holding more than the cap would otherwise be
				// counted whole, because the walk only comes back around
				// between directories.
				if msg.scan.total() >= deleteScanCap {
					msg.scan.truncated = true
					return msg
				}
				if child.IsDir() {
					msg.scan.folders++
					stack = append(stack, fs.Child(dir, child.Name))
					continue
				}
				msg.scan.files++
				msg.scan.bytes += child.Size
			}
		}
		return msg
	}
}

// deleteEvent is one batch of removals from a running delete. The producer
// coalesces them rather than sending a message per file — see
// deleteFlushInterval.
type deleteEvent struct {
	paths    []string // removed since the last batch, in the order removed
	failures []string // "path: reason", over the same window
	done     int      // cumulative removals
	failed   int      // cumulative failures
	err      error    // the first failure, kept for the closing status line
}

// deleteStreamClosed reports that the walk is over. The channel closing is the
// single terminal signal — whether the job finished, failed items along the
// way, or was cancelled — which is why no event carries a "done" flag that a
// cancel could race.
type deleteStreamClosed struct{}

// deleteTickMsg advances the pinned row's spinner.
type deleteTickMsg struct{}

func deleteTickCmd() tea.Cmd {
	return tea.Tick(deleteTickInterval, func(time.Time) tea.Msg { return deleteTickMsg{} })
}

// deleteJob is a recursive delete in flight. Unlike a transfer it is not a
// queue row: there is at most one at a time, and it is pinned above the bottom
// pane's tabs so it stays visible whichever tab is open.
type deleteJob struct {
	pane  paneID
	label string // what the user asked to delete, shown until the first removal
	total int    // from the scan; a lower bound when truncated
	// truncated means total undercounts, so the bar is held just short of
	// full rather than allowed to sit at 100% while the walk carries on.
	truncated bool
	done      int
	failed    int
	current   string // the path most recently removed
	firstErr  error
	canceled  bool
	cancel    context.CancelFunc
	events    <-chan deleteEvent
	startedAt time.Time
	frame     int
}

func (j *deleteJob) progress() float64 {
	if j.total <= 0 {
		return 0
	}
	p := float64(j.done+j.failed) / float64(j.total)
	if j.truncated {
		return min(p, 0.99)
	}
	return min(p, 1)
}

// totalLabel renders the denominator, marking a truncated count as a floor.
func (j *deleteJob) totalLabel() string {
	if j.truncated {
		return fmt.Sprintf("%d+", j.total)
	}
	return fmt.Sprintf("%d", j.total)
}

// deleteItem is one entry on the walk's stack. expanded marks a directory
// whose children have already been pushed, so the second time it comes off the
// stack it is empty and ready to remove.
type deleteItem struct {
	path     string
	isDir    bool
	expanded bool
}

// startDeleteJob begins a recursive delete and returns the job to hold in the
// model plus the commands that pump its events and spin its indicator.
func startDeleteJob(fs vfs.FS, base string, pane paneID, entries []domain.Entry, scan deleteScan, label string) (*deleteJob, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan deleteEvent, 256)
	job := &deleteJob{
		pane:      pane,
		label:     label,
		total:     scan.total(),
		truncated: scan.truncated,
		cancel:    cancel,
		events:    events,
		startedAt: time.Now(),
	}

	// Reversed, so the first thing the user selected is the first thing to go
	// and the progress row reads in the order they expect.
	stack := make([]deleteItem, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		if isParentDirEntry(entries[i]) {
			continue
		}
		stack = append(stack, deleteItem{path: fs.Child(base, entries[i].Name), isDir: entries[i].IsDir()})
	}

	go runDelete(ctx, fs, stack, events)
	return job, tea.Batch(waitForDeleteEvent(events), deleteTickCmd())
}

// runDelete removes everything on the stack and closes events when it is done.
//
// The walk is iterative and depth-first: every file and subdirectory goes
// before the directory that holds it, because vfs.FS.Remove — like rmdir —
// only takes an empty directory. Hidden entries are included; symlinks are
// removed as the link and never followed, so a link pointing back up its own
// tree can neither drive the walk forever nor let the delete escape what it
// was given.
//
// A failed removal is counted and the walk continues, matching syncPruneCmd:
// one unwritable file should not strand every item after it. Each List and
// Remove carries its own deadline; the walk as a whole carries only ctx, so
// cancelling is the one thing that stops it early.
//
// Sends block. The model keeps waitForDeleteEvent re-issued until this channel
// closes — cancelling does not stop it draining — so there is always a reader.
func runDelete(ctx context.Context, fs vfs.FS, stack []deleteItem, events chan<- deleteEvent) {
	defer close(events)

	var (
		pending   deleteEvent
		done      int
		failed    int
		firstErr  error
		lastFlush = time.Now()
	)

	flush := func() {
		pending.done, pending.failed, pending.err = done, failed, firstErr
		events <- pending
		pending = deleteEvent{}
		lastFlush = time.Now()
	}

	record := func(path string, err error) {
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			pending.failures = append(pending.failures, path+": "+err.Error())
		} else {
			done++
			pending.paths = append(pending.paths, path)
		}
		if len(pending.paths)+len(pending.failures) >= deleteFlushCount || time.Since(lastFlush) >= deleteFlushInterval {
			flush()
		}
	}

	for len(stack) > 0 && ctx.Err() == nil {
		item := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if item.isDir && !item.expanded {
			children, err := listForDelete(ctx, fs, item.path)
			if err != nil {
				// Unreadable, so it cannot be emptied either. Count it once
				// with the error that explains why, rather than pushing a
				// removal certain to fail for a second, vaguer reason.
				record(item.path, err)
				continue
			}
			// Back under its own children, so it is removed last.
			item.expanded = true
			stack = append(stack, item)
			for _, child := range children {
				if isParentDirEntry(child) {
					continue
				}
				stack = append(stack, deleteItem{path: fs.Child(item.path, child.Name), isDir: child.IsDir()})
			}
			continue
		}

		record(item.path, removeForDelete(ctx, fs, item.path))
	}

	flush()
}

func listForDelete(ctx context.Context, fs vfs.FS, dir string) ([]domain.Entry, error) {
	stepCtx, cancel := context.WithTimeout(ctx, deleteStepTimeout)
	defer cancel()
	return fs.List(stepCtx, dir, true)
}

func removeForDelete(ctx context.Context, fs vfs.FS, target string) error {
	stepCtx, cancel := context.WithTimeout(ctx, deleteStepTimeout)
	defer cancel()
	return fs.Remove(stepCtx, target)
}

// waitForDeleteEvent blocks a command goroutine on the job's event channel and
// delivers the next batch as a message — the same pump waitForTransferEvent
// runs over transfer.Event. Update re-issues it after each batch, and the
// channel closing ends both the pump and the job.
func waitForDeleteEvent(events <-chan deleteEvent) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return deleteStreamClosed{}
		}
		return event
	}
}

// applyDeleteScan opens the confirmation prompt once the count is in.
func (m *Model) applyDeleteScan(msg deleteScanMsg) {
	scan := msg.scan
	m.fileAction = &fileActionPrompt{kind: fileActionDelete, pane: msg.pane, entries: msg.entries, scan: &scan}
	m.overlay = overlayFileAction
	m.setStatus("delete " + deleteScanCounts(scan) + "?")
}

// deleteScanCounts states what a confirmed delete would actually remove. The
// old prompt said only "including 1 folder(s) and their contents", which reads
// the same for five files as for fifty thousand.
func deleteScanCounts(scan deleteScan) string {
	counts := fmt.Sprintf("%d file(s)", scan.files)
	if scan.folders > 0 {
		counts += fmt.Sprintf(" in %d folder(s)", scan.folders)
	}
	if scan.bytes > 0 {
		counts += " — " + formatSize(scan.bytes)
	}
	if scan.truncated {
		// Either number can be short, and a "+" on each one read as noise
		// against a folder count that happened to be exact. One qualifier over
		// the whole phrase says the same thing and says it once.
		return "at least " + counts
	}
	return counts
}

// deleteTargetLabel names what is being deleted, for the progress row before
// the first removal has landed.
func deleteTargetLabel(entries []domain.Entry) string {
	names := make([]string, 0, 3)
	for _, entry := range entries {
		if isParentDirEntry(entry) {
			continue
		}
		if len(names) == 3 {
			return strings.Join(names, ", ") + ", …"
		}
		names = append(names, entry.Name)
	}
	return strings.Join(names, ", ")
}

// startDelete turns a confirmed delete prompt into a running job.
func (m *Model) startDelete(prompt fileActionPrompt) tea.Cmd {
	fs := m.fsByID(prompt.pane)
	pane := m.filePaneByID(prompt.pane)
	if fs == nil || pane == nil {
		m.setError("not connected")
		return nil
	}
	scan := deleteScan{}
	if prompt.scan != nil {
		scan = *prompt.scan
	} else {
		// A selection of plain files is never scanned: the listing already
		// knows exactly how many items and how many bytes are going.
		for _, entry := range prompt.entries {
			if isParentDirEntry(entry) {
				continue
			}
			scan.files++
			scan.bytes += entry.Size
		}
	}
	job, cmd := startDeleteJob(fs, pane.path, prompt.pane, prompt.entries, scan, deleteTargetLabel(prompt.entries))
	m.deleteJob = job
	m.appendLog(fmt.Sprintf("delete %s under %s", deleteScanCounts(scan), pane.path))
	m.setStatus(fmt.Sprintf("deleting %s item(s) — x cancels", job.totalLabel()))
	return cmd
}

// applyDeleteEvent folds one batch of removals into the running job and the
// Log tab.
func (m *Model) applyDeleteEvent(msg deleteEvent) tea.Cmd {
	job := m.deleteJob
	if job == nil {
		// A stale batch from a job already retired; there is nothing left to
		// pump it into.
		return nil
	}
	job.done, job.failed = msg.done, msg.failed
	if job.firstErr == nil {
		job.firstErr = msg.err
	}
	lines := make([]string, 0, len(msg.paths)+len(msg.failures))
	for _, path := range msg.paths {
		lines = append(lines, "deleted "+path)
	}
	for _, failure := range msg.failures {
		lines = append(lines, "delete failed: "+failure)
	}
	m.appendLog(lines...)
	if len(msg.paths) > 0 {
		job.current = msg.paths[len(msg.paths)-1]
	} else if len(msg.failures) > 0 {
		job.current = msg.failures[len(msg.failures)-1]
	}
	return waitForDeleteEvent(job.events)
}

// applyDeleteDone retires the job once its event stream closes — the one
// signal that the walk is over, however it ended.
func (m *Model) applyDeleteDone() tea.Cmd {
	job := m.deleteJob
	if job == nil {
		return nil
	}
	m.deleteJob = nil
	job.cancel() // release the context whatever ended the walk
	elapsed := deleteElapsed(time.Since(job.startedAt))
	switch {
	case job.canceled:
		m.appendLog(fmt.Sprintf("delete cancelled — %d removed, %d failed", job.done, job.failed))
		m.setStatus(fmt.Sprintf("delete cancelled — %d item(s) removed", job.done))
	case job.failed > 0:
		// setError logs this itself, so there is no appendLog here.
		m.setError(fmt.Sprintf("deleted %d, %d failed — see log (%v)", job.done, job.failed, job.firstErr))
	default:
		m.appendLog(fmt.Sprintf("deleted %d item(s) in %s", job.done, elapsed))
		m.setStatus(fmt.Sprintf("deleted %d item(s) in %s", job.done, elapsed))
	}
	// The pane is stale either way: a cancelled or partly failed delete still
	// removed everything up to the point it stopped, and the selection names
	// entries that may no longer be there.
	pane := m.filePaneByID(job.pane)
	if pane == nil {
		return nil
	}
	pane.selected = map[string]bool{}
	return m.requestListing(job.pane, pane.path, listingRefresh)
}

// deleteElapsed renders how long a delete took. Rounding everything to the
// second reported a fast local delete as "0s", which reads as though it never
// ran at all.
func deleteElapsed(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

// applyDeleteTick advances the spinner, and lets the tick chain lapse once the
// job is gone.
func (m *Model) applyDeleteTick() tea.Cmd {
	if m.deleteJob == nil {
		return nil
	}
	m.deleteJob.frame++
	return deleteTickCmd()
}

// cancelDeleteJob stops a running delete, reporting whether there was one. The
// job is deliberately not cleared here: the walk keeps draining its channel
// until it closes, and that close is what retires it — see applyDeleteDone.
func (m *Model) cancelDeleteJob() bool {
	if m.deleteJob == nil {
		return false
	}
	m.deleteJob.canceled = true
	m.deleteJob.cancel()
	m.setStatus("cancelling delete…")
	return true
}

// deleteSpinnerFrames and deleteSpinnerASCII are the pinned row's activity
// indicator, one per glyph mode.
var (
	deleteSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	deleteSpinnerASCII  = []string{"|", "/", "-", "\\"}
)

// deleteRows is how many rows the pinned progress block takes right now: the
// one number renderBottomPane and bottomVisibleRows both read, so the renderer
// and the scroll arithmetic cannot disagree about it.
func (m Model) deleteRows() int {
	if m.deleteJob == nil {
		return 0
	}
	return deleteRowHeight
}

// renderDeleteRows draws the pinned progress block: a counter, a bar in the
// same 18-column form the transfer rows use, and the path being removed right
// now. It sits above the tab rows rather than inside one tab, so a delete
// stays visible wherever the user is looking.
func (m Model) renderDeleteRows(renderer tideui.Renderer, width int) []string {
	job := m.deleteJob
	if job == nil {
		return nil
	}
	bg, fg := rowSurface(renderer, renderer.Styles.DetailBody)
	accent := renderer.Styles.Theme.BorderFocus
	verb := "Deleting"
	if job.canceled {
		accent, verb = renderer.Styles.Theme.Dimmed, "Cancelling"
	}

	const barWidth = 18
	progress := job.progress()
	filled := min(barWidth, max(0, int(progress*float64(barWidth))))
	bar := segment(bg, fg, "[") +
		segment(bg, accent, strings.Repeat("=", filled)) +
		segment(bg, fg, strings.Repeat(" ", barWidth-filled)) +
		segment(bg, fg, "]")

	spinner := m.glyph(renderer,
		deleteSpinnerFrames[job.frame%len(deleteSpinnerFrames)],
		deleteSpinnerASCII[job.frame%len(deleteSpinnerASCII)])

	counter := fmt.Sprintf("%s  %d/%s", verb, job.done+job.failed, job.totalLabel())
	right := segment(bg, accent, fmt.Sprintf("%3.0f%%  x cancel", progress*100))
	left := segment(bg, accent, spinner+" ") + segment(bg, fg, counter+"  ") + bar
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	head := clampView(left+segment(bg, fg, strings.Repeat(" ", gap))+right, width, 1, bg)

	detail := job.current
	if detail == "" {
		detail = job.label
	}
	if job.failed > 0 {
		detail = fmt.Sprintf("%s   (%d failed)", detail, job.failed)
	}
	body := clampView(segment(bg, renderer.Styles.Theme.Dimmed, "  "+short(detail, max(4, width-2))), width, 1, bg)
	return []string{head, body}
}
