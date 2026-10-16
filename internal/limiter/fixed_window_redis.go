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

type RedisFixedWindow struct {
	client redis.Scripter
	script *redis.Script
}

func NewRedisFixedWindow(client redis.Scripter) *RedisFixedWindow {
	return &RedisFixedWindow{
		client: client,
		script: redis.NewScript(fixedWindowSource),
	}
}

func (r *RedisFixedWindow) Allow(ctx context.Context, key string, rule Rule) (Decision, error) {
	keys := []string{fixedWindowKey(key)}
	args := []any{rule.Limit, rule.Window.Milliseconds()}

	values, err := r.script.Run(ctx, r.client, keys, args...).Int64Slice()
	if err != nil {
		return Decision{}, err
	}
	return decisionFromScript(values)
}

func fixedWindowKey(apiKey string) string {
	return "hg:fw:{" + apiKey + "}"
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
