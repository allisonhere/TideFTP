package transfer

import (
	"sync"
	"testing"
	"time"
)

// fakeClock lets a test drive the limiter's notion of time: sleeping advances
// the clock instead of blocking.
type fakeClock struct {
	mu  sync.Mutex
	t   time.Time
	log []time.Duration
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) sleep(d time.Duration, _, _ <-chan struct{}) bool {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.log = append(c.log, d)
	c.mu.Unlock()
	return true
}

func newTestLimiter(rate int64) (*Limiter, *fakeClock) {
	c := &fakeClock{t: time.Unix(1000, 0)}
	l := NewLimiter(rate)
	l.now, l.sleep = c.now, c.sleep
	return l, c
}

func TestNilLimiterIsUnlimited(t *testing.T) {
	var l *Limiter
	if !l.Wait(1<<30, nil, nil) {
		t.Fatal("nil limiter should never block or cancel")
	}
	if NewLimiter(0) != nil || NewLimiter(-5) != nil {
		t.Fatal("a non-positive rate should mean unlimited (nil)")
	}
}

func TestLimiterHoldsTheAverageRate(t *testing.T) {
	l, clock := newTestLimiter(1000) // 1000 B/s
	start := clock.now()
	for i := 0; i < 10; i++ {
		l.Wait(500, nil, nil) // 5000 bytes in total
	}
	// The last 500 bytes are booked but not yet sent when Wait returns, so
	// after 10 waits the clock sits at (5000-500)/1000 = 4.5s.
	if got := clock.now().Sub(start); got != 4500*time.Millisecond {
		t.Fatalf("elapsed = %v, want 4.5s", got)
	}
}

func TestLimiterDoesNotBankIdleTime(t *testing.T) {
	l, clock := newTestLimiter(1000)
	l.Wait(1000, nil, nil)
	clock.mu.Lock()
	clock.t = clock.t.Add(time.Hour) // a long pause
	clock.mu.Unlock()
	before := len(clock.log)
	l.Wait(1000, nil, nil) // first chunk after the pause goes straight out
	if len(clock.log) != before {
		t.Fatalf("idle credit was banked: slept %v", clock.log[before:])
	}
	l.Wait(1000, nil, nil)
	if len(clock.log) != before+1 || clock.log[before] != time.Second {
		t.Fatalf("expected a 1s wait for the next chunk, got %v", clock.log[before:])
	}
}

func TestSharedLimiterCapsCombinedRate(t *testing.T) {
	l, clock := newTestLimiter(1000)
	start := clock.now()
	for i := 0; i < 4; i++ { // two "transfers" interleaving on one limiter
		l.Wait(500, nil, nil)
	}
	if got := clock.now().Sub(start); got != 1500*time.Millisecond {
		t.Fatalf("elapsed = %v, want 1.5s for 2000 bytes at 1000 B/s", got)
	}
}

func TestLimiterWaitIsCancellable(t *testing.T) {
	l := NewLimiter(1) // 1 B/s: the second wait would take forever
	stop := make(chan struct{})
	l.Wait(1, stop, nil)
	done := make(chan bool)
	go func() { done <- l.Wait(1, stop, nil) }()
	time.Sleep(20 * time.Millisecond)
	close(stop)
	select {
	case ok := <-done:
		if ok {
			t.Fatal("Wait should report cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after stop closed")
	}
}
