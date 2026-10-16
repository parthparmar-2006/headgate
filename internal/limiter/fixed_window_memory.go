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

func (m *MemoryFixedWindow) Allow(ctx context.Context, key string, rule Rule) (Decision, error) {
	now := m.clock.Now()
	windowStart := now.Truncate(rule.Window)
	windowEnd := windowStart.Add(rule.Window)

	m.mu.Lock()
	defer m.mu.Unlock()

	counter := m.counterFor(key, windowStart)
	if counter.count >= rule.Limit {
		return Decision{Allowed: false, Remaining: 0, RetryAfter: windowEnd.Sub(now), DecidedAt: now}, nil
	}

	counter.count++
	return Decision{Allowed: true, Remaining: rule.Limit - counter.count, DecidedAt: now}, nil
}

func (m *MemoryFixedWindow) counterFor(key string, windowStart time.Time) *windowCounter {
	counter, found := m.counters[key]
	if !found || !counter.windowStart.Equal(windowStart) {
		counter = &windowCounter{windowStart: windowStart}
		m.counters[key] = counter
	}
	return counter
}
