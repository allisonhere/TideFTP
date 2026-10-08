package transfer

import (
	"sync"
	"time"
)

// Limiter paces byte movement to a steady rate. One Limiter shared by several
// transfers is a total cap across all of them (lftp's limit-total-rate); a
// Limiter per transfer is a per-transfer cap (limit-rate).
//
// It reserves time rather than banking tokens: each Wait books the next slice
// of the link and sleeps until it starts, so the average rate is exact, there
// is no burst beyond one chunk, and concurrent waiters queue fairly. A nil
// *Limiter means "unlimited" and every method is a no-op, so callers need no
// nil checks.
type Limiter struct {
	mu   sync.Mutex
	rate float64   // bytes per second
	next time.Time // when the link is next free

	now   func() time.Time
	sleep func(d time.Duration, stop, quit <-chan struct{}) bool
}

// NewLimiter returns a limiter for bytesPerSec, or nil (unlimited) when the
// rate is not positive.
func NewLimiter(bytesPerSec int64) *Limiter {
	if bytesPerSec <= 0 {
		return nil
	}
	return &Limiter{rate: float64(bytesPerSec), now: time.Now, sleep: sleepUnlessCanceled}
}

// Wait books n bytes and blocks until they may be sent. It returns false when
// stop or quit closed first, in which case the caller should abandon the
// transfer as cancelled.
func (l *Limiter) Wait(n int, stop, quit <-chan struct{}) bool {
	if l == nil || n <= 0 {
		return true
	}
	l.mu.Lock()
	now := l.now()
	if l.next.Before(now) {
		l.next = now // an idle link does not bank credit
	}
	wait := l.next.Sub(now)
	l.next = l.next.Add(time.Duration(float64(n) / l.rate * float64(time.Second)))
	l.mu.Unlock()

	if wait <= 0 {
		return true
	}
	return l.sleep(wait, stop, quit)
}

func sleepUnlessCanceled(d time.Duration, stop, quit <-chan struct{}) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-stop:
		return false
	case <-quit:
		return false
	}
}
