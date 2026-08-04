package server

import (
	"sync"
	"time"
)

// bucket is one key's token bucket: tokens refill continuously at rate/min up
// to the burst ceiling (the full minute's allowance).
type bucket struct {
	tokens float64
	last   time.Time
}

// rateLimiter is a per-key token bucket guarding /v1 from runaway clients on
// the public internet. Keys are bearer tokens (or client IPs when anonymous);
// idle buckets are swept opportunistically so the map can't grow unbounded.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	perMin  float64
	now     func() time.Time // injectable clock for tests
}

func newRateLimiter(perMin int) *rateLimiter {
	return &rateLimiter{buckets: map[string]*bucket{}, perMin: float64(perMin), now: time.Now}
}

func (l *rateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.perMin, last: t}
		l.buckets[key] = b
	}
	// Refill for elapsed time, then sweep buckets idle long enough to be full
	// again — they're indistinguishable from fresh ones.
	b.tokens = min(l.perMin, b.tokens+t.Sub(b.last).Seconds()*l.perMin/60)
	b.last = t
	if len(l.buckets) > 1024 {
		for k, ob := range l.buckets {
			if t.Sub(ob.last) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
