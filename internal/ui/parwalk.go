package ui

import (
	"context"
	"sync"

	"tideftp/internal/domain"
)

// defaultScanWorkers is how many directories a tree walk lists at once by
// default. Listing is latency-bound, so a few in flight cuts a big scan
// roughly in proportion. It stays below the FTP pool size (4) so a
// connection is left for browsing.
const defaultScanWorkers = 3

// walkParallel lists a directory tree with up to workers listings in flight.
// handle runs only on the calling goroutine, once per finished listing, so
// it may touch caller state freely; it returns the sub-directories to walk
// next and whether to stop early. Completion order is not DFS order.
//
// It returns how many queued directories were never listed (0 when the walk
// finished), so a caller can tell a truncated walk from a complete one.
func walkParallel[T any](
	ctx context.Context,
	roots []T,
	workers int,
	list func(context.Context, T) ([]domain.Entry, error),
	handle func(item T, children []domain.Entry, err error) (next []T, stop bool),
) (unlisted int) {
	if workers < 1 {
		workers = 1
	}
	ctx, cancel := context.WithCancel(ctx)

	type result struct {
		item     T
		children []domain.Entry
		err      error
	}
	jobs := make(chan T)
	results := make(chan result)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				children, err := list(ctx, item)
				select {
				case results <- result{item, children, err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	defer func() {
		cancel()
		close(jobs)
		wg.Wait()
	}()

	pending := append([]T(nil), roots...)
	inflight := 0
	for len(pending) > 0 || inflight > 0 {
		var send chan T
		var next T
		if len(pending) > 0 {
			send, next = jobs, pending[len(pending)-1]
		}
		select {
		case send <- next:
			pending = pending[:len(pending)-1]
			inflight++
		case r := <-results:
			inflight--
			more, stop := handle(r.item, r.children, r.err)
			if stop {
				return len(pending) + len(more) + inflight
			}
			pending = append(pending, more...)
		case <-ctx.Done():
			return len(pending) + inflight
		}
	}
	return 0
}
