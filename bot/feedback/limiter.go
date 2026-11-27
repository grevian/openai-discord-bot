package feedback

import (
	"sync"
	"time"
)

// Limiter allows each user a number of items per sliding window.
type Limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	now    func() time.Time
	seen   map[string][]time.Time
}

// NewLimiter allows max items per user in any window.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, now: time.Now, seen: map[string][]time.Time{}}
}

// Allow records an item for user and reports whether it is within the limit.
// A refused item is not recorded.
func (l *Limiter) Allow(user string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-l.window)
	// Drop users with nothing recent, so the map cannot grow without bound.
	for u, ts := range l.seen {
		if !ts[len(ts)-1].After(cutoff) {
			delete(l.seen, u)
		}
	}
	recent := l.seen[user][:0:0]
	for _, t := range l.seen[user] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	if len(recent) >= l.max {
		l.seen[user] = recent
		return false
	}
	l.seen[user] = append(recent, now)
	return true
}
