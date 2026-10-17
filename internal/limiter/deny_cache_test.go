package limiter

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

type scriptedLimiter struct {
	decision Decision
	err      error
	calls    int
}

func (s *scriptedLimiter) Allow(ctx context.Context, checks []Check) (Decision, error) {
	s.calls++
	return s.decision, s.err
}

var cacheRule = Rule{Limit: 10, Window: time.Second}

func TestDenyCacheServesRepeatDeniesLocally(t *testing.T) {
	clock := newFakeClock()
	inner := &scriptedLimiter{decision: Decision{Allowed: false, RetryAfter: 300 * time.Millisecond}}
	cache := NewDenyCache(inner, clock, 100)

	cache.Allow(context.Background(), single("k", cacheRule))
	clock.Advance(100 * time.Millisecond)
	decision, err := cache.Allow(context.Background(), single("k", cacheRule))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Allowed {
		t.Fatalf("cached deny was allowed")
	}
	if decision.RetryAfter != 200*time.Millisecond {
		t.Fatalf("retry after %v, want 200ms", decision.RetryAfter)
	}
	if inner.calls != 1 {
		t.Fatalf("inner called %d times, want 1", inner.calls)
	}
}

func TestDenyCacheAsksAgainWhenRetryTimeArrives(t *testing.T) {
	clock := newFakeClock()
	inner := &scriptedLimiter{decision: Decision{Allowed: false, RetryAfter: 300 * time.Millisecond}}
	cache := NewDenyCache(inner, clock, 100)

	cache.Allow(context.Background(), single("k", cacheRule))
	clock.Advance(300 * time.Millisecond)
	cache.Allow(context.Background(), single("k", cacheRule))

	if inner.calls != 2 {
		t.Fatalf("inner called %d times, want 2", inner.calls)
	}
}

func TestDenyCacheNeverCachesAllows(t *testing.T) {
	clock := newFakeClock()
	inner := &scriptedLimiter{decision: Decision{Allowed: true, Remaining: 5}}
	cache := NewDenyCache(inner, clock, 100)

	for range 5 {
		cache.Allow(context.Background(), single("k", cacheRule))
	}

	if inner.calls != 5 {
		t.Fatalf("inner called %d times, want 5", inner.calls)
	}
}

func TestDenyCacheNeverCachesErrors(t *testing.T) {
	clock := newFakeClock()
	inner := &scriptedLimiter{err: errors.New("redis down")}
	cache := NewDenyCache(inner, clock, 100)

	_, first := cache.Allow(context.Background(), single("k", cacheRule))
	_, second := cache.Allow(context.Background(), single("k", cacheRule))

	if first == nil || second == nil {
		t.Fatalf("errors must pass through, got %v and %v", first, second)
	}
	if inner.calls != 2 {
		t.Fatalf("inner called %d times, want 2", inner.calls)
	}
}

func TestDenyCacheKeysAreIndependent(t *testing.T) {
	clock := newFakeClock()
	inner := &scriptedLimiter{decision: Decision{Allowed: false, RetryAfter: time.Second}}
	cache := NewDenyCache(inner, clock, 100)

	cache.Allow(context.Background(), single("a", cacheRule))
	cache.Allow(context.Background(), single("b", cacheRule))

	if inner.calls != 2 {
		t.Fatalf("inner called %d times, want 2", inner.calls)
	}
}

func TestDenyCacheStaysWithinMaxKeys(t *testing.T) {
	clock := newFakeClock()
	inner := &scriptedLimiter{decision: Decision{Allowed: false, RetryAfter: time.Minute}}
	cache := NewDenyCache(inner, clock, 10)

	for i := range 50 {
		cache.Allow(context.Background(), single("k"+strconv.Itoa(i), cacheRule))
	}

	if cache.Size() != 10 {
		t.Fatalf("size %d, want 10", cache.Size())
	}
}

func TestDenyCacheSweepsExpiredEntriesWhenFull(t *testing.T) {
	clock := newFakeClock()
	inner := &scriptedLimiter{decision: Decision{Allowed: false, RetryAfter: 100 * time.Millisecond}}
	cache := NewDenyCache(inner, clock, 10)

	for i := range 10 {
		cache.Allow(context.Background(), single("old"+strconv.Itoa(i), cacheRule))
	}
	clock.Advance(2 * time.Second)
	cache.Allow(context.Background(), single("new", cacheRule))

	if cache.Size() != 1 {
		t.Fatalf("size %d, want 1 after sweeping 10 expired entries", cache.Size())
	}
}
