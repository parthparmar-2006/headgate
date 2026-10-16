package limiter

import (
	"context"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("HEADGATE_REDIS_ADDR")
	if addr == "" {
		t.Skip("HEADGATE_REDIS_ADDR not set")
	}

	client := redis.NewClient(&redis.Options{Addr: addr, PoolSize: 200})
	t.Cleanup(func() { client.Close() })

	err := client.Ping(context.Background()).Err()
	if err != nil {
		t.Fatalf("redis not reachable at %s: %v", addr, err)
	}
	return client
}

func uniqueKey(t *testing.T) string {
	return t.Name() + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

func allowConcurrently(t *testing.T, l Limiter, key string, rule Rule, workers int, perWorker int) int64 {
	t.Helper()
	var allowed atomic.Int64
	var wg sync.WaitGroup

	for range workers {
		wg.Go(func() {
			for range perWorker {
				decision, err := l.Allow(context.Background(), key, rule)
				if err != nil {
					t.Errorf("unexpected error: %v", err)
					return
				}
				if decision.Allowed {
					allowed.Add(1)
				}
			}
		})
	}

	wg.Wait()
	return allowed.Load()
}

func TestRedisFixedWindowAllowsExactlyLimit(t *testing.T) {
	l := NewRedisFixedWindow(newTestRedis(t))
	rule := Rule{Limit: 5, Window: time.Hour}

	allowed := allowN(t, l, uniqueKey(t), rule, 20)

	if allowed != 5 {
		t.Fatalf("allowed %d, want 5", allowed)
	}
}

func TestRedisFixedWindowKeysAreIndependent(t *testing.T) {
	l := NewRedisFixedWindow(newTestRedis(t))
	rule := Rule{Limit: 2, Window: time.Hour}
	keyA := uniqueKey(t) + "-a"
	keyB := uniqueKey(t) + "-b"

	allowN(t, l, keyA, rule, 10)

	if allowN(t, l, keyB, rule, 10) != 2 {
		t.Fatalf("key b was affected by key a")
	}
}

func TestRedisFixedWindowResetsAfterWindow(t *testing.T) {
	l := NewRedisFixedWindow(newTestRedis(t))
	rule := Rule{Limit: 3, Window: 200 * time.Millisecond}
	key := uniqueKey(t)

	allowN(t, l, key, rule, 3)
	time.Sleep(rule.Window)

	if allowN(t, l, key, rule, 10) != 3 {
		t.Fatalf("new window did not allow a full limit")
	}
}

func TestRedisFixedWindowDeniedHasRetryAfterWithinWindow(t *testing.T) {
	l := NewRedisFixedWindow(newTestRedis(t))
	rule := Rule{Limit: 1, Window: time.Hour}
	key := uniqueKey(t)

	allowN(t, l, key, rule, 1)
	decision, err := l.Allow(context.Background(), key, rule)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if decision.Allowed {
		t.Fatalf("expected deny")
	}
	if decision.RetryAfter <= 0 || decision.RetryAfter > rule.Window {
		t.Fatalf("retry after %v outside (0, %v]", decision.RetryAfter, rule.Window)
	}
	drift := time.Since(decision.DecidedAt)
	if drift < -time.Second || drift > time.Second {
		t.Fatalf("decided at %v is %v away from local time", decision.DecidedAt, drift)
	}
}

func TestRedisFixedWindowIsAtomicUnderConcurrency(t *testing.T) {
	l := NewRedisFixedWindow(newTestRedis(t))
	rule := Rule{Limit: 100, Window: time.Hour}

	allowed := allowConcurrently(t, l, uniqueKey(t), rule, 100, 20)

	if allowed != 100 {
		t.Fatalf("allowed %d of 2000 concurrent requests, want exactly 100", allowed)
	}
}
