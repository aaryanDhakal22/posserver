package touch

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOncePerPeriodPerID(t *testing.T) {
	l := New(time.Minute)
	now := time.Unix(1_000_000, 0)
	l.now = func() time.Time { return now }
	a, b := uuid.New(), uuid.New()
	if !l.Allow(a) || !l.Allow(b) {
		t.Fatal("first stamp of each id must be allowed")
	}
	for i := 0; i < 1000; i++ {
		if l.Allow(a) {
			t.Fatal("a second stamp within the period was allowed")
		}
	}
	now = now.Add(time.Minute)
	if !l.Allow(a) {
		t.Fatal("a stamp after the period was refused")
	}
}

func TestDoBoundsGoroutinesAndSetsDeadline(t *testing.T) {
	l := New(time.Minute)
	id := uuid.New()
	var calls atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)
	for i := 0; i < 1000; i++ {
		l.Do(id, func(ctx context.Context) error {
			defer wg.Done()
			calls.Add(1)
			if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > Timeout {
				t.Errorf("no %v deadline on the write", Timeout)
			}
			return nil
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("writes = %d, want 1", calls.Load())
	}
}

func TestPrunesPastCap(t *testing.T) {
	l := New(time.Minute)
	now := time.Unix(1_000_000, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < maxIDs; i++ {
		l.Allow(uuid.New())
	}
	now = now.Add(2 * time.Minute)
	l.Allow(uuid.New())
	if len(l.last) != 1 {
		t.Fatalf("remembered %d ids after the prune, want 1", len(l.last))
	}
}
