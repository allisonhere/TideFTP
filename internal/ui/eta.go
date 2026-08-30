package ui

import (
	"fmt"
	"time"

	"tideftp/internal/domain"
)

// Estimating time remaining needs a throughput to divide by, and the app
// keeps no per-transfer history to derive one from — the Stats tab's samples
// are session-wide and only collected while that tab is open. So a row's
// rate is its whole-life average: bytes moved since it started, over how
// long it has been running. That is steadier than a short window (a stalled
// chunk does not make the ETA leap to hours) at the cost of reacting slowly
// to a link that genuinely changed speed halfway through, which is the right
// trade for a number a person glances at.
//
// Nothing here schedules a redraw. It does not need to: progress events
// arrive every transfer.ProgressInterval while anything is moving, and each
// one repaints the transfer pane, so a live ETA ticks down on its own and
// costs no timer of its own.

// transferRate is a transfer's average throughput in bytes/sec, and whether
// there was enough to measure. Bytes already on the far side when the
// transfer started (a resume) are excluded: they were not moved by this
// transfer and counting them would inflate its rate.
func transferRate(t domain.Transfer, now time.Time) (float64, bool) {
	if t.Status != domain.Active || t.StartedAt.IsZero() {
		return 0, false
	}
	moved := t.BytesDone - t.ResumeFrom
	elapsed := now.Sub(t.StartedAt).Seconds()
	if moved <= 0 || elapsed <= 0 {
		return 0, false
	}
	return float64(moved) / elapsed, true
}

// transferETA is how much longer an active transfer has to run. It is
// unknowable — reported false — before the first bytes move, for a transfer
// whose total size the listing never gave (BytesTotal 0, which an FTP LIST
// parser can leave behind), and for one that is already done but has not
// been told so yet.
func transferETA(t domain.Transfer, now time.Time) (time.Duration, bool) {
	rate, ok := transferRate(t, now)
	if !ok || t.BytesTotal <= 0 {
		return 0, false
	}
	remaining := t.BytesTotal - t.BytesDone
	if remaining <= 0 {
		return 0, false
	}
	return time.Duration(float64(remaining) / rate * float64(time.Second)), true
}

// overallETA is how long everything still queued or running will take,
// assuming the transfers now in flight keep the pace they have been
// managing. It deliberately does not model the queue draining into freed
// slots — that would need to predict how fast transfers not yet started
// will run — so it reads as an optimistic floor on a long queue and an
// accurate figure on a short one.
func (m Model) overallETA(now time.Time) (time.Duration, bool) {
	var remaining int64
	var rate float64
	for _, t := range m.transfers {
		switch t.Status {
		case domain.Active:
			remaining += max(0, t.BytesTotal-t.BytesDone)
			if r, ok := transferRate(t, now); ok {
				rate += r
			}
		case domain.Queued:
			remaining += max(0, t.BytesTotal-t.BytesDone)
		}
	}
	if remaining <= 0 || rate <= 0 {
		return 0, false
	}
	return time.Duration(float64(remaining) / rate * float64(time.Second)), true
}

// formatETA renders a duration as m:ss, or h:mm:ss once it passes an hour.
// Anything beyond a day is reported as "1d+" rather than a precise number
// nobody would trust or wait for.
func formatETA(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d >= 24*time.Hour {
		return "1d+"
	}
	total := int(d.Round(time.Second).Seconds())
	hours, minutes, seconds := total/3600, total/60%60, total%60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%d:%02d", minutes, seconds)
}

// transferMetaLabel is the text a transfer row shows to the right of its
// percentage: live throughput and time remaining while it is running and
// there is enough to measure, and otherwise the row's own status message,
// which is what every non-active row has to say for itself anyway.
func transferMetaLabel(t domain.Transfer, now time.Time) string {
	rate, ok := transferRate(t, now)
	if !ok {
		return statusMessage(t)
	}
	if eta, ok := transferETA(t, now); ok {
		return fmt.Sprintf("%s ETA %s", formatRate(int64(rate)), formatETA(eta))
	}
	return formatRate(int64(rate))
}

// statusMessage is a transfer's own message, falling back to its status when
// it has not been given one.
func statusMessage(t domain.Transfer) string {
	if t.Message != "" {
		return t.Message
	}
	return transferStatus(t.Status)
}

// bottomPaneHint is the transfers pane header's right-hand text: which tab
// is showing, and — whenever there is work in flight worth estimating — how
// long all of it has left.
func (m Model) bottomPaneHint(now time.Time) string {
	hint := m.bottomTabLabel()
	if eta, ok := m.overallETA(now); ok {
		hint += " · ETA " + formatETA(eta)
	}
	return hint
}
