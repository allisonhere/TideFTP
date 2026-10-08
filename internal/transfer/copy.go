package transfer

import (
	"context"
	"errors"
	"sync/atomic"
)

// copySeq gives every synchronous Copy a unique request ID, so a caller never
// has to invent one and two copies in a row cannot collide in the Engine.
var copySeq atomic.Int64

// Copy runs one request to completion on eng and blocks until that request's
// terminal event. The request's ID is assigned here and ignored if set.
// onProgress, when non-nil, is called with the running byte count.
//
// This is the synchronous seam the non-interactive CLI needs. The UI drives
// Engine directly — its progress has to land on the Bubble Tea goroutine, which
// is why Engine is asynchronous in the first place — but a script only wants
// "move this, and tell me if it worked".
//
// Copy owns eng.Events() for its duration and must not run against an Engine
// something else is also reading events from. Cancelling ctx asks the engine to
// stop the transfer and returns ctx.Err().
func Copy(ctx context.Context, eng Engine, req Request, onProgress func(done int64)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	req.ID = int(copySeq.Add(1))
	if onProgress == nil {
		onProgress = func(int64) {}
	}

	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			eng.Cancel(req.ID)
		case <-finished:
		}
	}()

	eng.Start(req)
	for ev := range eng.Events() {
		if ev.ID != req.ID {
			continue
		}
		switch ev.Kind {
		case Progress:
			onProgress(ev.BytesDone)
		case Completed:
			return nil
		case Failed:
			return ev.Err
		case Canceled:
			if err := ctx.Err(); err != nil {
				return err
			}
			return ErrCanceled
		}
	}
	return errors.New("transfer engine closed before the transfer finished")
}
