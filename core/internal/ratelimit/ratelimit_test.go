package ratelimit

import (
	"testing"
	"time"
)

// SEC-SHR-8: a caller that exceeds its rate is refused until tokens have refilled, and callers do not share buckets.
func TestBucketRefillsAndKeysAreIndependent(t *testing.T) {
	now := time.Unix(1000, 0)
	l := New(60, 3) // one unit per second, bursts of three
	l.now = func() time.Time { return now }
	for i := range 3 {
		if !l.Allow("a") {
			t.Fatalf("burst unit %d refused", i)
		}
	}
	if l.Allow("a") {
		t.Fatal("the burst must be exhausted")
	}
	if !l.Allow("b") {
		t.Fatal("another key has its own bucket")
	}
	now = now.Add(2 * time.Second)
	granted := 0
	for range 5 {
		if l.Allow("a") {
			granted++
		}
	}
	if granted != 2 {
		t.Fatalf("two seconds must refill exactly two units, got %d", granted)
	}
	if got := l.RetryAfter("a", 1); got < time.Second || got > 2*time.Second {
		t.Fatalf("retry after %v", got)
	}
}

func TestOversizedRequestsAreNeverAllowed(t *testing.T) {
	l := New(600, 10)
	if l.AllowN("x", 11) {
		t.Fatal("more than the burst can never be paid for")
	}
	if !l.AllowN("x", 10) {
		t.Fatal("exactly the burst is fine")
	}
}

func TestIdleKeysAreForgotten(t *testing.T) {
	now := time.Unix(1000, 0)
	l := New(60, 2)
	l.now = func() time.Time { return now }
	for i := range 100 {
		l.Allow(string(rune('a' + i%26)))
	}
	now = now.Add(5 * time.Minute)
	l.Allow("z")
	if len(l.buckets) > 2 {
		t.Fatalf("%d buckets kept after everyone went idle", len(l.buckets))
	}
}
