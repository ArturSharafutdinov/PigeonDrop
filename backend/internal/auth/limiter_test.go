package auth

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newClock() *clock { return &clock{t: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)} }

func TestLimiterBlocksAfterTheAllowedFailures(t *testing.T) {
	c := newClock()
	l := NewLimiter(3, 15*time.Minute, c.now)

	for i := range 3 {
		if _, blocked := l.Blocked("a"); blocked {
			t.Fatalf("blocked before failure %d", i+1)
		}
		l.Fail("a")
	}
	retry, blocked := l.Blocked("a")
	if !blocked || retry != 15*time.Minute {
		t.Fatalf("after 3 failures: blocked=%v retry=%v", blocked, retry)
	}

	c.advance(10 * time.Minute)
	if retry, blocked := l.Blocked("a"); !blocked || retry != 5*time.Minute {
		t.Fatalf("10 minutes later: blocked=%v retry=%v", blocked, retry)
	}

	// More failures while blocked do not extend the block: the window started with the first failure.
	l.Fail("a")
	if retry, _ := l.Blocked("a"); retry != 5*time.Minute {
		t.Fatalf("a failure while blocked moved the end to %v", retry)
	}

	c.advance(5 * time.Minute)
	if _, blocked := l.Blocked("a"); blocked {
		t.Fatal("still blocked after the window")
	}
	l.Fail("a")
	if _, blocked := l.Blocked("a"); blocked {
		t.Fatal("one failure in a new window blocked, so the old count was kept")
	}
}

func TestLimiterKeysAreIndependentAndResettable(t *testing.T) {
	l := NewLimiter(2, time.Hour, newClock().now)
	l.Fail("a")
	l.Fail("a")
	if _, blocked := l.Blocked("b"); blocked {
		t.Fatal("b was blocked by a's failures")
	}
	if _, blocked := l.Blocked("a"); !blocked {
		t.Fatal("a was not blocked")
	}
	l.Reset("a")
	if _, blocked := l.Blocked("a"); blocked {
		t.Fatal("a still blocked after a reset")
	}
	l.Reset("never seen") // must not panic
}

func TestLimiterFailuresOutsideTheWindowDoNotAddUp(t *testing.T) {
	c := newClock()
	l := NewLimiter(3, time.Hour, c.now)
	for range 10 {
		l.Fail("slow")
		c.advance(61 * time.Minute) // one failure an hour never reaches three in a window
	}
	if _, blocked := l.Blocked("slow"); blocked {
		t.Fatal("scattered failures blocked a key")
	}
}

func TestLimiterMemoryIsBounded(t *testing.T) {
	l := NewLimiter(5, time.Hour, newClock().now)
	for i := range maxEntries + 500 {
		l.Fail(fmt.Sprintf("key-%d", i))
	}
	l.mu.Lock()
	n := len(l.entries)
	l.mu.Unlock()
	if n > maxEntries {
		t.Fatalf("%d entries kept, limit %d", n, maxEntries)
	}
}

func TestLimiterExpiredEntriesAreDropped(t *testing.T) {
	c := newClock()
	l := NewLimiter(5, time.Minute, c.now)
	for i := range 100 {
		l.Fail(fmt.Sprintf("k%d", i))
	}
	c.advance(2 * time.Minute)
	l.Fail("fresh")
	l.mu.Lock()
	n := len(l.entries)
	l.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d entries after everything expired, want 1", n)
	}
}

func TestLimiterIsSafeForConcurrentUse(t *testing.T) {
	l := NewLimiter(1000, time.Hour, nil)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 200 {
				key := fmt.Sprintf("k%d", (i+j)%7)
				l.Blocked(key)
				l.Fail(key)
				if j%50 == 0 {
					l.Reset(key)
				}
			}
		}()
	}
	wg.Wait()
}
