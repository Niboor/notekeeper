// Package ratelimit is a small in-memory token-bucket limiter keyed by string. It protects the
// unauthenticated share endpoints (SEC-SHR-8) and is deliberately per replica: it bounds what one
// process spends on one caller, while limits that must hold across replicas (login attempts,
// link creation) live in PostgreSQL.
package ratelimit

import (
	"sync"
	"time"
)

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter allows `rate` units per second per key with a burst of `burst` units.
type Limiter struct {
	rate, burst float64
	now         func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

// New creates a limiter allowing perMinute units per minute per key, in bursts of up to burst.
func New(perMinute, burst float64) *Limiter {
	return &Limiter{rate: perMinute / 60, burst: burst, now: time.Now, buckets: map[string]*bucket{}}
}

// Allow takes one unit.
func (l *Limiter) Allow(key string) bool { return l.AllowN(key, 1) }

// AllowN takes n units (bytes, for a bandwidth ceiling) and reports whether the key had them. A
// request larger than the burst is never allowed: it could never be paid for.
func (l *Limiter) AllowN(key string, n float64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if n > b.tokens {
		return false
	}
	b.tokens -= n
	return true
}

// RetryAfter is how long until n units are available for the key, for the Retry-After header.
func (l *Limiter) RetryAfter(key string, n float64) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok || l.rate == 0 {
		return time.Second
	}
	missing := n - b.tokens
	if missing <= 0 {
		return time.Second
	}
	return max(time.Second, time.Duration(missing/l.rate*float64(time.Second)))
}

// sweep drops keys that are full again (they carry no information) so the map cannot grow without bound.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < time.Minute && len(l.buckets) < 50_000 {
		return
	}
	l.lastSweep = now
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, k)
		}
	}
}
