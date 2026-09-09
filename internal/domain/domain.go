package domain

import "time"

type EntryKind int

const (
	EntryFile EntryKind = iota
	EntryDir
	EntrySymlink
)

type Entry struct {
	Name     string
	Kind     EntryKind
	Size     int64
	Mode     string
	Modified time.Time
	Hidden   bool
	// LinksToDir is set on an EntrySymlink whose target resolves to a
	// directory. An adapter that cannot resolve the target cheaply — or a
	// link that dangles — leaves it false, so false means "not known to be
	// a directory" rather than "known not to be one".
	LinksToDir bool
}

// IsDir reports whether the entry is a real directory. It is deliberately
// false for a symlink pointing at one: everything that walks or deletes a
// tree keys off this, and following a link there would let a cycle drive the
// walk forever and let a recursive delete escape the tree it was given. Use
// IsDirLike for the "can I open this?" question instead.
func (e Entry) IsDir() bool { return e.Kind == EntryDir }

// IsDirLike reports whether the entry can be opened as a directory — a real
// one, or a symlink known to point at one. Navigation uses it; tree walks
// and recursive deletes must not.
func (e Entry) IsDirLike() bool { return e.Kind == EntryDir || e.LinksToDir }

type TransferDirection int

const (
	Upload TransferDirection = iota
	Download
)

type TransferStatus int

const (
	Queued TransferStatus = iota
	Active
	Failed
	Done
	Canceled
)

type Transfer struct {
	ID          int
	Direction   TransferDirection
	Source      string
	Destination string
	BytesTotal  int64
	BytesDone   int64
	// ResumeFrom is the byte offset this transfer started from — non-zero
	// only when it was queued to resume a partial destination file. 0 means
	// an ordinary full transfer.
	ResumeFrom int64
	Status     TransferStatus
	Message    string
	StartedAt  time.Time
	FinishedAt time.Time
	// Protocol is the connection protocol ("sftp", "ftp", "ftps") this
	// transfer ran over, captured at queue time. The connection's own
	// protocol can change on reconnect, after which it would no longer
	// describe transfers already sitting in the queue — this field is what
	// lets a per-protocol breakdown stay correct across a reconnect.
	Protocol string
}

func (t Transfer) Progress() float64 {
	if t.BytesTotal <= 0 {
		return 0
	}
	return float64(t.BytesDone) / float64(t.BytesTotal)
}
