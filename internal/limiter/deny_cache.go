package limiter

import (
	"context"
	"sync"
	"time"
)

const sweepInterval = time.Second

type DenyCache struct {
	inner       Limiter
	clock       Clock
	maxKeys     int
	mu          sync.Mutex
	deniedUntil map[string]time.Time
	lastSweep   time.Time
}

func NewDenyCache(inner Limiter, clock Clock, maxKeys int) *DenyCache {
	return &DenyCache{
		inner:       inner,
		clock:       clock,
		maxKeys:     maxKeys,
		deniedUntil: make(map[string]time.Time),
	}
}

func (c *DenyCache) Allow(ctx context.Context, key string, rule Rule) (Decision, error) {
	now := c.clock.Now()
	until, cached := c.lookup(key, now)
	if cached {
		return Decision{Allowed: false, RetryAfter: until.Sub(now), DecidedAt: now}, nil
	}

	decision, err := c.inner.Allow(ctx, key, rule)
	if err != nil {
		return decision, err
	}
	if !decision.Allowed && decision.RetryAfter > 0 {
		c.remember(key, c.clock.Now().Add(decision.RetryAfter))
	}
	return decision, nil
}

func (c *DenyCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.deniedUntil)
}

func (c *DenyCache) lookup(key string, now time.Time) (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	until, found := c.deniedUntil[key]
	if !found {
		return time.Time{}, false
	}
	if !now.Before(until) {
		delete(c.deniedUntil, key)
		return time.Time{}, false
	}
	return until, true
}

func (c *DenyCache) remember(key string, until time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.deniedUntil) >= c.maxKeys {
		c.sweepIfDue()
	}
	if len(c.deniedUntil) >= c.maxKeys {
		return
	}
	c.deniedUntil[key] = until
}

func (c *DenyCache) sweepIfDue() {
	now := c.clock.Now()
	if now.Sub(c.lastSweep) < sweepInterval {
		return
	}
	c.lastSweep = now

	for key, until := range c.deniedUntil {
		if !now.Before(until) {
			delete(c.deniedUntil, key)
		}
	}
}
