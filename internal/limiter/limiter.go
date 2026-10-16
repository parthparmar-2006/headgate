package limiter

import (
	"context"
	"time"
)

type Rule struct {
	Limit  int
	Window time.Duration
}

type Decision struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration
	DecidedAt  time.Time
}

type Limiter interface {
	Allow(ctx context.Context, key string, rule Rule) (Decision, error)
}

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now()
}
