package limiter

import (
	"time"

	"github.com/redis/go-redis/v9"
)

func NewRedisClient(addr string, minIdleConns int) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:                  addr,
		ContextTimeoutEnabled: true,
		MaxRetries:            -1,
		DialTimeout:           time.Second,
		MinIdleConns:          minIdleConns,
	})
}
