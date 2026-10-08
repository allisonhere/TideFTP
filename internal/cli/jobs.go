package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// job is one background command (`cmd &`) or the batch of `queue`d commands.
type job struct {
	id      int
	line    string
	done    chan struct{}
	cancel  func() // closes the job's own connection, which aborts what it is doing
	killed  bool
	pending func() int // for the queue: how many commands are still waiting
}

// jobTable tracks a script's background work.
type jobTable struct {
	mu        sync.Mutex
	next      int
	jobs      map[int]*job
	firstFail int // first non-zero exit code from a job that was not killed

	queue *jobQueue
}

func newJobTable() *jobTable {
	return &jobTable{jobs: map[int]*job{}, next: 1}
}

func (t *jobTable) add(line string) *job {
	t.mu.Lock()
	defer t.mu.Unlock()
	j := &job{id: t.next, line: line, done: make(chan struct{})}
	t.next++
	t.jobs[j.id] = j
	return j
}

// finish records a job's result, reports it, and releases waiters.
func (t *jobTable) finish(app App, j *job, err error) {
	t.mu.Lock()
	killed := j.killed
	delete(t.jobs, j.id)
	t.mu.Unlock()

	switch {
	case killed:
		if !app.shared.quiet {
			app.statusf("[%d] Killed: %s", j.id, j.line)
		}
	case err != nil:
		code := app.report(err)
		t.mu.Lock()
		if t.firstFail == 0 {
			t.firstFail = code
		}
		t.mu.Unlock()
		app.statusf("[%d] Failed: %s", j.id, j.line)
	default:
		if !app.shared.quiet {
			app.statusf("[%d] Done: %s", j.id, j.line)
		}
	}
	close(j.done)
}

func (t *jobTable) snapshot() []*job {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*job, 0, len(t.jobs))
	for _, j := range t.jobs {
		out = append(out, j)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].id < out[b].id })
	return out
}

func (t *jobTable) list(app App) {
	for _, j := range t.snapshot() {
		extra := ""
		if j.pending != nil {
			extra = fmt.Sprintf(" (%d waiting)", j.pending())
		}
		app.resultf("[%d] Running  %s%s", j.id, j.line, extra)
	}
}

// wait blocks until job N (or, with no argument, every job) has finished.
func (t *jobTable) wait(app App, args []string) error {
	if len(args) == 0 {
		t.waitAll(app)
		return nil
	}
	if len(args) > 1 {
		return usageError("wait takes at most one job number")
	}
	id, err := strconv.Atoi(strings.TrimPrefix(args[0], "%"))
	if err != nil {
		return usageError("wait: %q is not a job number", args[0])
	}
	t.mu.Lock()
	j := t.jobs[id]
	t.mu.Unlock()
	if j == nil {
		return usageError("wait: no running job %d", id)
	}
	<-j.done
	return nil
}

// waitAll blocks until nothing is running and returns the first failure code.
func (t *jobTable) waitAll(app App) int {
	for {
		running := t.snapshot()
		if len(running) == 0 {
			break
		}
		for _, j := range running {
			<-j.done
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.firstFail
}

func (t *jobTable) kill(args []string) error {
	if len(args) != 1 {
		return usageError("kill takes a job number or `all`")
	}
	if args[0] == "all" {
		t.killAll()
		return nil
	}
	id, err := strconv.Atoi(strings.TrimPrefix(args[0], "%"))
	if err != nil {
		return usageError("kill: %q is not a job number", args[0])
	}
	t.mu.Lock()
	j := t.jobs[id]
	if j != nil {
		j.killed = true
	}
	t.mu.Unlock()
	if j == nil {
		return usageError("kill: no running job %d", id)
	}
	if j.cancel != nil {
		j.cancel()
	}
	return nil
}

func (t *jobTable) killAll() {
	for _, j := range t.snapshot() {
		t.mu.Lock()
		j.killed = true
		t.mu.Unlock()
		if j.cancel != nil {
			j.cancel()
		}
	}
}

// childSession gives a background job its own connection, starting in the
// current directory.
func (s *scriptRun) childSession() (*sharedSession, error) {
	sh := s.app.shared
	if sh.conn == nil {
		return nil, usageError("not connected: use open HOST|PROFILE first")
	}
	conn, err := s.app.connectLive(sh.target, sh.creds, sh.flags)
	if err != nil {
		return nil, err
	}
	return &sharedSession{
		conn: conn, target: sh.target, cwd: sh.cwd, quiet: sh.quiet, limit: sh.limit,
		creds: sh.creds, flags: sh.flags, settings: sh.settings,
	}, nil
}

// jobRunner is a scriptRun for a background job: the same aliases and job
// table, but the job's own session.
func (s *scriptRun) jobRunner(child *sharedSession) *scriptRun {
	app := s.app
	app.shared = child
	aliases := make(map[string][]string, len(s.aliases))
	for k, v := range s.aliases {
		aliases[k] = v
	}
	return &scriptRun{app: app, keepGoing: s.keepGoing, aliases: aliases, jobs: s.jobs, depth: s.depth}
}

// startJob runs words in the background on a connection of its own.
func (s *scriptRun) startJob(words []string) error {
	child, err := s.childSession()
	if err != nil {
		return err
	}
	j := s.jobs.add(strings.Join(words, " "))
	j.cancel = func() { _ = child.conn.Close() }
	runner := s.jobRunner(child)
	go func() {
		err := runner.runOne(runner.app, words[0], words[1:])
		_ = child.conn.Close()
		s.jobs.finish(runner.app, j, err)
	}()
	return nil
}

// jobQueue runs `queue`d commands one after another in the background, on a
// single connection of its own, like lftp's queue.
type jobQueue struct {
	mu      sync.Mutex
	items   []queuedCmd
	running bool
}

type queuedCmd struct {
	words []string
	cwd   string
}

func (s *scriptRun) queueJob(args []string) error {
	if len(args) == 0 {
		s.jobs.list(s.app)
		return nil
	}
	sh := s.app.shared
	if sh.conn == nil {
		return usageError("not connected: use open HOST|PROFILE first")
	}
	t := s.jobs
	t.mu.Lock()
	if t.queue == nil {
		t.queue = &jobQueue{}
	}
	q := t.queue
	t.mu.Unlock()

	q.mu.Lock()
	q.items = append(q.items, queuedCmd{words: append([]string(nil), args...), cwd: sh.cwd})
	start := !q.running
	q.running = true
	q.mu.Unlock()
	if start {
		return s.startQueueWorker(q)
	}
	return nil
}

func (s *scriptRun) startQueueWorker(q *jobQueue) error {
	child, err := s.childSession()
	if err != nil {
		q.mu.Lock()
		q.items, q.running = nil, false
		q.mu.Unlock()
		return err
	}
	j := s.jobs.add("queue")
	j.pending = func() int {
		q.mu.Lock()
		defer q.mu.Unlock()
		return len(q.items)
	}
	j.cancel = func() {
		q.mu.Lock()
		q.items = nil
		q.mu.Unlock()
		_ = child.conn.Close()
	}
	runner := s.jobRunner(child)
	go func() {
		var firstErr error
		for {
			q.mu.Lock()
			if len(q.items) == 0 {
				q.running = false
				q.mu.Unlock()
				break
			}
			item := q.items[0]
			q.items = q.items[1:]
			q.mu.Unlock()

			child.cwd = item.cwd
			if err := runner.runOne(runner.app, item.words[0], item.words[1:]); err != nil {
				runner.app.report(err)
				if firstErr == nil {
					firstErr = fmt.Errorf("queued command %q failed", strings.Join(item.words, " "))
				}
			}
		}
		_ = child.conn.Close()
		s.jobs.finish(runner.app, j, firstErr)
	}()
	return nil
}
