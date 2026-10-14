package ui

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"tideftp/internal/domain"
)

func TestWalkParallelVisitsAllAndOverlaps(t *testing.T) {
	var cur, peak atomic.Int32
	list := func(_ context.Context, dir string) ([]domain.Entry, error) {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		cur.Add(-1)
		if len(dir) >= 4 {
			return nil, nil
		}
		return []domain.Entry{{Name: "a"}, {Name: "b"}, {Name: "c"}}, nil
	}
	seen := 0
	left := walkParallel(context.Background(), []string{"/"}, 4, list,
		func(dir string, kids []domain.Entry, err error) ([]string, bool) {
			seen++
			var next []string
			for _, k := range kids {
				next = append(next, dir+k.Name)
			}
			return next, false
		})
	if left != 0 || seen != 1+3+9+27 {
		t.Fatalf("left=%d seen=%d", left, seen)
	}
	if peak.Load() < 2 {
		t.Fatalf("listings never overlapped (peak %d)", peak.Load())
	}
}

func TestWalkParallelStopReportsUnlisted(t *testing.T) {
	list := func(context.Context, int) ([]domain.Entry, error) { return nil, nil }
	left := walkParallel(context.Background(), []int{1, 2, 3, 4, 5}, 1, list,
		func(int, []domain.Entry, error) ([]int, bool) { return nil, true })
	if left == 0 {
		t.Fatal(fmt.Sprint("expected unlisted dirs after early stop"))
	}
}
