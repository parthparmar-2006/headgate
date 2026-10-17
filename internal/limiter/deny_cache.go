package limiter

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const sweepInterval = time.Second

type DenyCache struct {
	inner       Limiter
	clock       Clock
	maxKeys     int
	mu          sync.Mutex
	deniedUntil map[string]cachedDenial
	lastSweep   time.Time
	hits        atomic.Int64
}

type cachedDenial struct {
	until      time.Time
	checkIndex int
}

func NewDenyCache(inner Limiter, clock Clock, maxKeys int) *DenyCache {
	return &DenyCache{
		inner:       inner,
		clock:       clock,
		maxKeys:     maxKeys,
		deniedUntil: make(map[string]cachedDenial),
	}
}

func (c *DenyCache) Allow(ctx context.Context, checks []Check) (Decision, error) {
	cacheKey := cacheKeyFor(checks)
	now := c.clock.Now()
	denial, cached := c.lookup(cacheKey, now)
	if cached {
		c.hits.Add(1)
		decision := Decision{
			Allowed:    false,
			RetryAfter: denial.until.Sub(now),
			DecidedAt:  now,
			CheckIndex: denial.checkIndex,
		}
		return decision, nil
	}

	decision, err := c.inner.Allow(ctx, checks)
	if err != nil {
		return decision, err
	}
	if !decision.Allowed && decision.RetryAfter > 0 {
		denial := cachedDenial{
			until:      c.clock.Now().Add(decision.RetryAfter),
			checkIndex: decision.CheckIndex,
		}
		c.remember(cacheKey, denial)
	}
	return decision, nil
}

func (c *DenyCache) Hits() int64 {
	return c.hits.Load()
}

func (c *DenyCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.deniedUntil)
}

func cacheKeyFor(checks []Check) string {
	keys := make([]string, len(checks))
	for i, check := range checks {
		keys[i] = check.Key
	}
	return strings.Join(keys, "|")
}

func (c *DenyCache) lookup(cacheKey string, now time.Time) (cachedDenial, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	denial, found := c.deniedUntil[cacheKey]
	if !found {
		return cachedDenial{}, false
	}
	if !now.Before(denial.until) {
		delete(c.deniedUntil, cacheKey)
		return cachedDenial{}, false
	}
	return denial, true
}

func (c *DenyCache) remember(cacheKey string, denial cachedDenial) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.deniedUntil) >= c.maxKeys {
		c.sweepIfDue()
	}
	if len(c.deniedUntil) >= c.maxKeys {
		return
	}
	c.deniedUntil[cacheKey] = denial
}

func (c *DenyCache) sweepIfDue() {
	now := c.clock.Now()
	if now.Sub(c.lastSweep) < sweepInterval {
		return
	}
	c.lastSweep = now

	for cacheKey, denial := range c.deniedUntil {
		if !now.Before(denial.until) {
			delete(c.deniedUntil, cacheKey)
		}
	}
}
