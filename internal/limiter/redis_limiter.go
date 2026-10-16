package limiter

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed lua/fixed_window.lua
var fixedWindowSource string

//go:embed lua/sliding_log.lua
var slidingLogSource string

//go:embed lua/sliding_counter.lua
var slidingCounterSource string

type RedisLimiter struct {
	client    redis.Scripter
	script    *redis.Script
	keyPrefix string
}

func NewRedisFixedWindow(client redis.Scripter) *RedisLimiter {
	return newRedisLimiter(client, fixedWindowSource, "hg:fw:")
}

func NewRedisSlidingLog(client redis.Scripter) *RedisLimiter {
	return newRedisLimiter(client, slidingLogSource, "hg:sl:")
}

func NewRedisSlidingCounter(client redis.Scripter) *RedisLimiter {
	return newRedisLimiter(client, slidingCounterSource, "hg:sc:")
}

func newRedisLimiter(client redis.Scripter, source string, keyPrefix string) *RedisLimiter {
	return &RedisLimiter{
		client:    client,
		script:    redis.NewScript(source),
		keyPrefix: keyPrefix,
	}
}

func (r *RedisLimiter) Allow(ctx context.Context, key string, rule Rule) (Decision, error) {
	keys := []string{r.RedisKey(key)}
	args := []any{rule.Limit, rule.Window.Milliseconds()}

	values, err := r.script.Run(ctx, r.client, keys, args...).Int64Slice()
	if err != nil {
		return Decision{}, err
	}
	return decisionFromScript(values)
}

func (r *RedisLimiter) RedisKey(apiKey string) string {
	return r.keyPrefix + "{" + apiKey + "}"
}

func decisionFromScript(values []int64) (Decision, error) {
	if len(values) != 4 {
		return Decision{}, fmt.Errorf("unexpected script reply length %d", len(values))
	}

	decision := Decision{
		Allowed:    values[0] == 1,
		Remaining:  int(values[1]),
		RetryAfter: time.Duration(values[2]) * time.Millisecond,
		DecidedAt:  time.UnixMilli(values[3]),
	}
	return decision, nil
}
