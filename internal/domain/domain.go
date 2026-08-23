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
}

func (e Entry) IsDir() bool { return e.Kind == EntryDir }

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
