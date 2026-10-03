package auth

import (
	"sync"
	"time"
)

// Limiter refuses further attempts for a key once it has failed too often in a window. It lives in the
// server's memory: a restart forgives everyone, and several server instances would each keep their own
// count. Both are fine for one small server; a shared store is the fix when that stops being true.
type Limiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]*attempts
	purged  time.Time
}

type attempts struct {
	failures int
	since    time.Time
}

// maxEntries bounds the memory a flood of distinct keys can take.
const maxEntries = 100_000

// NewLimiter allows limit failures per key within window. now may be nil (time.Now).
func NewLimiter(limit int, window time.Duration, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{limit: limit, window: window, now: now, entries: make(map[string]*attempts)}
}

// Blocked reports whether key has used up its failures, and how long until it may try again.
func (l *Limiter) Blocked(key string) (retryAfter time.Duration, blocked bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil {
		return 0, false
	}
	now := l.now()
	if now.Sub(e.since) >= l.window {
		delete(l.entries, key)
		return 0, false
	}
	if e.failures < l.limit {
		return 0, false
	}
	return e.since.Add(l.window).Sub(now), true
}

// Fail records one failed attempt for key.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.purge(now)

	e := l.entries[key]
	if e == nil || now.Sub(e.since) >= l.window {
		e = &attempts{since: now}
		l.entries[key] = e
	}
	e.failures++
}

// Reset forgets key's failures, for example after a successful sign-in.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// purge drops expired entries (at most once a window, or when full) and, if a flood still fills the
// map, arbitrary ones down to 90% of the limit, so the sweep runs once per thousands of failures and not
// on every one. Forgetting a key only ever lets someone try again, never locks anyone out.
func (l *Limiter) purge(now time.Time) {
	if len(l.entries) < maxEntries && now.Sub(l.purged) < l.window {
		return
	}
	l.purged = now
	for k, e := range l.entries {
		if now.Sub(e.since) >= l.window {
			delete(l.entries, k)
		}
	}
	for k := range l.entries {
		if len(l.entries) <= maxEntries/10*9 {
			break
		}
		delete(l.entries, k)
	}
}
