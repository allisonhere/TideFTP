// Package transfer defines the protocol-agnostic interface the UI uses to move
// bytes between the local filesystem and a remote server. FTP, FTPS, and SFTP
// engines will implement it alongside the existing fake one, the same way
// FTP/FTPS/SFTP adapters will implement vfs.FS for browsing.
//
// Engines are asynchronous by contract. Start must return immediately and
// report everything that happens afterwards on the Events channel: a real
// transfer blocks on the network for minutes at a time, so no part of it may
// run on the UI goroutine. This is the whole reason the interface looks the
// way it does — a synchronous Transfer(src, dst) error would freeze the TUI.
package transfer

import (
	"errors"
	"fmt"

	"tideftp/internal/domain"
)

// Request describes one file to move. ID matches domain.Transfer.ID so events
// coming back can be matched to the queue row that produced them.
type Request struct {
	ID          int
	Direction   domain.TransferDirection
	Source      string
	Destination string
	Size        int64
	// Offset is where to start moving bytes from — the source is read
	// starting here and the destination is written starting here too,
	// without truncating it first. 0 means an ordinary full transfer.
	Offset int64
	// Length, when positive, limits a download to the byte range
	// [Offset, Offset+Length) so several connections can each fetch a segment
	// of one file. 0 means "to the end of the file".
	Length int64
	// NoTruncate stops a download from truncating its destination even at
	// Offset 0, which is how parallel segments write into one preallocated file.
	NoTruncate bool
	// Limit, when non-nil, paces the transfer. Share one Limiter between
	// requests to cap their combined rate.
	Limit *Limiter
}

// EventKind is what just happened to a transfer.
type EventKind int

const (
	// Progress reports how many bytes have moved so far.
	Progress EventKind = iota
	// Completed means every byte arrived.
	Completed
	// Failed means the transfer stopped short; Event.Err says why.
	Failed
	// Canceled means the transfer stopped because Cancel was called.
	Canceled
)

// Event is one update about a transfer, identified by its Request ID.
// Progress events may arrive at any rate; the UI treats them as advisory and
// keeps its own notion of the total size.
type Event struct {
	ID        int
	Kind      EventKind
	BytesDone int64
	Err       error
}

// Terminal reports whether an event is the last one for its transfer.
func (e Event) Terminal() bool {
	return e.Kind == Completed || e.Kind == Failed || e.Kind == Canceled
}

// ErrShort marks a transfer that ended without error but moved fewer bytes
// than its Request said it would.
var ErrShort = errors.New("short transfer")

// CheckComplete guards the one failure a copy loop cannot see on its own: a
// source that stopped early looks exactly like a source that ended, so an
// engine reaching EOF has no way to tell a finished download from a
// truncated one. Comparing what actually moved against the size the listing
// reported is what separates them, and without it a truncated file is
// reported Completed — and drawn at 100%, since the UI pins BytesDone to
// BytesTotal on completion.
//
// A Request with no size (Size <= 0) is exempt: an FTP listing that could
// not parse a size reports 0 for a file that is not empty, and failing every
// such transfer would be worse than not checking. A transfer that moved more
// than expected is exempt too — that means the source grew after it was
// listed, which is not a failure.
func CheckComplete(req Request, sent int64) error {
	want := req.Size
	if req.Length > 0 {
		want = req.Offset + req.Length // a segment is complete at its own end
	}
	if want <= 0 || sent >= want {
		return nil
	}
	return fmt.Errorf("%w: moved %d of %d bytes", ErrShort, sent, want)
}

// SegmentEnd is where a ranged request stops (exclusive), or -1 for "to EOF".
func (r Request) SegmentEnd() int64 {
	if r.Length > 0 {
		return r.Offset + r.Length
	}
	return -1
}

// Engine moves bytes on behalf of the UI.
//
// Implementations own concurrency internally but do not queue: the caller
// decides what runs and when, and hands over one Request per running transfer.
// Every Request that Start accepts must eventually produce exactly one
// terminal event (Completed, Failed, or Canceled) on Events, or the caller's
// queue will stall waiting for a slot to free up.
type Engine interface {
	// Start begins a transfer. It must not block.
	Start(req Request)
	// Cancel asks an in-flight transfer to stop. Canceling an unknown or
	// already-finished ID is a no-op.
	Cancel(id int)
	// Events yields transfer events until Close, which closes the channel.
	Events() <-chan Event
	// Close stops every in-flight transfer, waits for them to report, and
	// closes the Events channel. It is safe to call more than once.
	Close() error
}
