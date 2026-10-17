package limiter

import (
	"context"
	"testing"
	"time"
)

type fakeClock struct {
	now time.Time
}

func (f *fakeClock) Now() time.Time {
	return f.now
}

func (f *fakeClock) Advance(d time.Duration) {
	f.now = f.now.Add(d)
}

func single(key string, rule Rule) []Check {
	return []Check{{Key: key, Rule: rule}}
}

func newFakeClock() *fakeClock {
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	return &fakeClock{now: start}
}

func allowN(t *testing.T, l Limiter, key string, rule Rule, n int) int {
	t.Helper()
	allowed := 0
	for range n {
		decision, err := l.Allow(context.Background(), single(key, rule))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if decision.Allowed {
			allowed++
		}
	}
	return allowed
}

func TestFixedWindowAllowsExactlyLimit(t *testing.T) {
	clock := newFakeClock()
	l := NewMemoryFixedWindow(clock)
	rule := Rule{Limit: 5, Window: time.Second}

	allowed := allowN(t, l, "key-a", rule, 20)

	if allowed != 5 {
		t.Fatalf("allowed %d, want 5", allowed)
	}
}

func TestFixedWindowRemainingCountsDown(t *testing.T) {
	clock := newFakeClock()
	l := NewMemoryFixedWindow(clock)
	rule := Rule{Limit: 3, Window: time.Second}

	for want := 2; want >= 0; want-- {
		decision, _ := l.Allow(context.Background(), single("key-a", rule))
		if decision.Remaining != want {
			t.Fatalf("remaining %d, want %d", decision.Remaining, want)
		}
	}
}

func TestFixedWindowResetsAtNextWindow(t *testing.T) {
	clock := newFakeClock()
	l := NewMemoryFixedWindow(clock)
	rule := Rule{Limit: 5, Window: time.Second}

	allowN(t, l, "key-a", rule, 5)
	clock.Advance(999 * time.Millisecond)
	if allowN(t, l, "key-a", rule, 1) != 0 {
		t.Fatalf("request allowed before window ended")
	}

	clock.Advance(time.Millisecond)
	if allowN(t, l, "key-a", rule, 10) != 5 {
		t.Fatalf("new window did not allow a full limit")
	}
}

func TestFixedWindowKeysAreIndependent(t *testing.T) {
	clock := newFakeClock()
	l := NewMemoryFixedWindow(clock)
	rule := Rule{Limit: 2, Window: time.Second}

	allowN(t, l, "key-a", rule, 10)

	if allowN(t, l, "key-b", rule, 10) != 2 {
		t.Fatalf("key-b was affected by key-a")
	}
}

func TestFixedWindowRetryAfterIsTimeToWindowEnd(t *testing.T) {
	clock := newFakeClock()
	l := NewMemoryFixedWindow(clock)
	rule := Rule{Limit: 1, Window: time.Second}

	allowN(t, l, "key-a", rule, 1)
	clock.Advance(300 * time.Millisecond)
	decision, _ := l.Allow(context.Background(), single("key-a", rule))

	if decision.Allowed {
		t.Fatalf("expected deny")
	}
	if decision.RetryAfter != 700*time.Millisecond {
		t.Fatalf("retry after %v, want 700ms", decision.RetryAfter)
	}
	if !decision.DecidedAt.Equal(clock.Now()) {
		t.Fatalf("decided at %v, want %v", decision.DecidedAt, clock.Now())
	}
}

func TestFixedWindowAllowsDoubleLimitAcrossBoundary(t *testing.T) {
	clock := newFakeClock()
	l := NewMemoryFixedWindow(clock)
	rule := Rule{Limit: 10, Window: time.Second}

	clock.Advance(990 * time.Millisecond)
	first := allowN(t, l, "key-a", rule, 10)
	clock.Advance(20 * time.Millisecond)
	second := allowN(t, l, "key-a", rule, 10)

	if first+second != 20 {
		t.Fatalf("allowed %d in 20ms, want 20 (known fixed window burst)", first+second)
	}
}

func TestFixedWindowMultiCheckIsAllOrNothing(t *testing.T) {
	clock := newFakeClock()
	l := NewMemoryFixedWindow(clock)
	wide := Check{Key: "wide", Rule: Rule{Limit: 5, Window: time.Second}}
	narrow := Check{Key: "narrow", Rule: Rule{Limit: 3, Window: time.Second}}

	allowedTogether := 0
	for range 10 {
		decision, _ := l.Allow(context.Background(), []Check{wide, narrow})
		if decision.Allowed {
			allowedTogether++
		}
	}
	allowedWideAlone := allowN(t, l, "wide", wide.Rule, 10)

	if allowedTogether != 3 || allowedWideAlone != 2 {
		t.Fatalf("together %d (want 3), wide alone %d (want 2)", allowedTogether, allowedWideAlone)
	}
}

func TestFixedWindowRejectsEmptyChecks(t *testing.T) {
	l := NewMemoryFixedWindow(newFakeClock())

	_, err := l.Allow(context.Background(), nil)

	if err != ErrNoChecks {
		t.Fatalf("error %v, want ErrNoChecks", err)
	}
}
