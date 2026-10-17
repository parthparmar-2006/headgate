package limiter

import (
	"context"
	"sync"
	"time"
)

type windowCounter struct {
	windowStart time.Time
	count       int
}

type MemoryFixedWindow struct {
	clock    Clock
	mu       sync.Mutex
	counters map[string]*windowCounter
}

func NewMemoryFixedWindow(clock Clock) *MemoryFixedWindow {
	return &MemoryFixedWindow{
		clock:    clock,
		counters: make(map[string]*windowCounter),
	}
}

func (m *MemoryFixedWindow) Allow(ctx context.Context, checks []Check) (Decision, error) {
	if len(checks) == 0 {
		return Decision{}, ErrNoChecks
	}
	now := m.clock.Now()

	m.mu.Lock()
	defer m.mu.Unlock()

	counters := make([]*windowCounter, len(checks))
	denied := Decision{Allowed: false, DecidedAt: now, CheckIndex: -1}
	for i, check := range checks {
		windowStart := now.Truncate(check.Rule.Window)
		counters[i] = m.counterFor(check.Key, windowStart)
		if counters[i].count >= check.Rule.Limit {
			retry := windowStart.Add(check.Rule.Window).Sub(now)
			denied = widenDenial(denied, i, retry)
		}
	}
	if denied.CheckIndex >= 0 {
		return denied, nil
	}

	allowed := Decision{Allowed: true, DecidedAt: now, Remaining: -1}
	for i, check := range checks {
		counters[i].count++
		remaining := check.Rule.Limit - counters[i].count
		allowed = narrowRemaining(allowed, i, remaining)
	}
	return allowed, nil
}

func (m *MemoryFixedWindow) counterFor(key string, windowStart time.Time) *windowCounter {
	counter, found := m.counters[key]
	if !found || !counter.windowStart.Equal(windowStart) {
		counter = &windowCounter{windowStart: windowStart}
		m.counters[key] = counter
	}
	return counter
}

func widenDenial(denied Decision, index int, retry time.Duration) Decision {
	if denied.CheckIndex < 0 {
		denied.CheckIndex = index
	}
	if retry > denied.RetryAfter {
		denied.RetryAfter = retry
	}
	return denied
}

func narrowRemaining(allowed Decision, index int, remaining int) Decision {
	if allowed.Remaining < 0 || remaining < allowed.Remaining {
		allowed.Remaining = remaining
		allowed.CheckIndex = index
	}
	return allowed
}
