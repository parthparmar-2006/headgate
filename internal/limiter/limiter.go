package limiter

import (
	"context"
	"errors"
	"time"
)

var ErrNoChecks = errors.New("no checks given")

type Rule struct {
	Limit  int
	Window time.Duration
}

type Check struct {
	Key  string
	Rule Rule
}

type Decision struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration
	DecidedAt  time.Time
	CheckIndex int
}

type Limiter interface {
	Allow(ctx context.Context, checks []Check) (Decision, error)
}

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now()
}
