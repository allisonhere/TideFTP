package ui

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/domain"
	"tideftp/internal/transfer"
	"tideftp/internal/vfs"
)

// verifyDoneMsg reports one completed transfer's checksum comparison.
type verifyDoneMsg struct {
	id    int
	match bool
	err   error
}

// verifyTimeout bounds one verification: both hashing passes together. It
// scales with the file, because verifying re-reads every byte that was just
// moved — a bound short enough to be useful on a 4 KB config would abandon a
// 2 GB archive halfway through. The floor covers connection setup and a
// small file; the rate assumed beyond it is deliberately pessimistic, since
// this exists to stop a hung read hanging around forever, not to enforce a
// throughput target.
func verifyTimeout(size int64) time.Duration {
	const assumedBytesPerSecond = 1 << 20 // 1 MiB/s
	// Two passes over the file, source and destination.
	budget := time.Duration(2*size/assumedBytesPerSecond) * time.Second
	return min(max(60*time.Second, budget), 2*time.Hour)
}

// beginVerify hashes both ends of a transfer that has just completed and
// reports whether they match. Nothing else in the app re-reads a transfer
// after the fact, which is the point: the engine can only report that every
// byte it read was written, never that what landed on the far side is what
// left. Only a second pass over both files can say that, and it costs
// exactly what it sounds like it costs — which is why the setting behind it
// is off by default.
func (m *Model) beginVerify(row domain.Transfer) tea.Cmd {
	srcFS, dstFS := m.localFS, m.remoteFS
	if row.Direction == domain.Download {
		srcFS, dstFS = m.remoteFS, m.localFS
	}
	if srcFS == nil || dstFS == nil {
		return nil
	}
	return verifyCmd(srcFS, dstFS, row.ID, row.Source, row.Destination, row.BytesTotal)
}

// verifyCmd hashes src then dst — one after the other rather than at once,
// so a verify never occupies two of an FTP pool's connections at the same
// time and cannot deadlock against the transfers still running beside it.
func verifyCmd(srcFS, dstFS vfs.FS, id int, src, dst string, size int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), verifyTimeout(size))
		defer cancel()

		srcSum, err := hashFile(ctx, srcFS, src)
		if err != nil {
			return verifyDoneMsg{id: id, err: fmt.Errorf("read %s: %w", src, err)}
		}
		dstSum, err := hashFile(ctx, dstFS, dst)
		if err != nil {
			return verifyDoneMsg{id: id, err: fmt.Errorf("read %s: %w", dst, err)}
		}
		return verifyDoneMsg{id: id, match: srcSum == dstSum}
	}
}

// hashFile streams a file through SHA-256 without ever holding more than a
// chunk of it in memory — the reason vfs.FS grew Open alongside ReadFile.
func hashFile(ctx context.Context, fs vfs.FS, path string) ([32]byte, error) {
	var sum [32]byte
	reader, err := fs.Open(ctx, path)
	if err != nil {
		return sum, err
	}
	defer reader.Close()

	hash := sha256.New()
	if _, err := io.CopyBuffer(hash, reader, make([]byte, transfer.CopyChunk)); err != nil {
		return sum, err
	}
	copy(sum[:], hash.Sum(nil))
	return sum, nil
}

// applyVerifyDone folds a verification result into the transfer it belongs
// to. A mismatch demotes the row from Done to Failed: the bytes moved, but
// what is on the far side is not what was sent, and a transfer whose result
// is wrong has not succeeded. Landing it in the Failed tab also makes it
// retryable with R, which is the only useful thing left to do about it.
//
// A verification that could not run at all — the connection dropped, the
// destination is unreadable — is reported without touching the row's
// status. Not being able to check is not the same as having checked and
// found a difference, and only one of those justifies calling a completed
// transfer failed.
func (m *Model) applyVerifyDone(msg verifyDoneMsg) {
	index := m.transferIndex(msg.id)
	if index < 0 {
		return
	}
	row := &m.transfers[index]
	switch {
	case msg.err != nil:
		row.Message = "unverified"
		m.setError(fmt.Sprintf("verify transfer %d: %v", row.ID, msg.err))
	case msg.match:
		row.Message = "verified"
		m.logs = append(m.logs, fmt.Sprintf("transfer %d verified (sha256)", row.ID))
	default:
		row.Status = domain.Failed
		row.Message = "checksum mismatch"
		m.setError(fmt.Sprintf("transfer %d: checksum mismatch", row.ID))
	}
}
