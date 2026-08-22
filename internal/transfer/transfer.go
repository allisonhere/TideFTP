// Package transfer defines the protocol-agnostic interface the UI uses to move
// bytes between the local filesystem and a remote server. FTP, FTPS, and SFTP
// engines will implement it alongside the existing fake one, the same way
// FTP/FTPS/SFTP adapters will implement remotefs.FS for browsing.
//
// Engines are asynchronous by contract. Start must return immediately and
// report everything that happens afterwards on the Events channel: a real
// transfer blocks on the network for minutes at a time, so no part of it may
// run on the UI goroutine. This is the whole reason the interface looks the
// way it does — a synchronous Transfer(src, dst) error would freeze the TUI.
package transfer

import "tideftp/internal/domain"

// Request describes one file to move. ID matches domain.Transfer.ID so events
// coming back can be matched to the queue row that produced them.
type Request struct {
	ID          int
	Direction   domain.TransferDirection
	Source      string
	Destination string
	Size        int64
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
