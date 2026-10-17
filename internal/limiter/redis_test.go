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

type redisAlgorithm struct {
	name  string
	build func(client redis.Scripter) *RedisLimiter
}

func redisAlgorithms() []redisAlgorithm {
	return []redisAlgorithm{
		{name: "fixed_window", build: NewRedisFixedWindow},
		{name: "sliding_log", build: NewRedisSlidingLog},
		{name: "sliding_counter", build: NewRedisSlidingCounter},
	}
}

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
				decision, err := l.Allow(context.Background(), single(key, rule))
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

func TestRedisAllowsExactlyLimit(t *testing.T) {
	client := newTestRedis(t)
	for _, algorithm := range redisAlgorithms() {
		t.Run(algorithm.name, func(t *testing.T) {
			l := algorithm.build(client)
			rule := Rule{Limit: 5, Window: time.Hour}

			allowed := allowN(t, l, uniqueKey(t), rule, 20)

			if allowed != 5 {
				t.Fatalf("allowed %d, want 5", allowed)
			}
		})
	}
}

func TestRedisKeysAreIndependent(t *testing.T) {
	client := newTestRedis(t)
	for _, algorithm := range redisAlgorithms() {
		t.Run(algorithm.name, func(t *testing.T) {
			l := algorithm.build(client)
			rule := Rule{Limit: 2, Window: time.Hour}
			keyA := uniqueKey(t) + "-a"
			keyB := uniqueKey(t) + "-b"

			allowN(t, l, keyA, rule, 10)

			if allowN(t, l, keyB, rule, 10) != 2 {
				t.Fatalf("key b was affected by key a")
			}
		})
	}
}

func TestRedisRecoversAfterIdle(t *testing.T) {
	client := newTestRedis(t)
	for _, algorithm := range redisAlgorithms() {
		t.Run(algorithm.name, func(t *testing.T) {
			l := algorithm.build(client)
			rule := Rule{Limit: 3, Window: 200 * time.Millisecond}
			key := uniqueKey(t)

			allowN(t, l, key, rule, 3)
			time.Sleep(2*rule.Window + 50*time.Millisecond)

			if allowN(t, l, key, rule, 10) != 3 {
				t.Fatalf("full limit not available after two idle windows")
			}
		})
	}
}

func TestRedisDeniedHasRetryAfterWithinWindow(t *testing.T) {
	client := newTestRedis(t)
	for _, algorithm := range redisAlgorithms() {
		t.Run(algorithm.name, func(t *testing.T) {
			l := algorithm.build(client)
			rule := Rule{Limit: 1, Window: time.Hour}
			key := uniqueKey(t)

			allowN(t, l, key, rule, 1)
			decision, err := l.Allow(context.Background(), single(key, rule))
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
		})
	}
}

func TestRedisIsAtomicUnderConcurrency(t *testing.T) {
	client := newTestRedis(t)
	for _, algorithm := range redisAlgorithms() {
		t.Run(algorithm.name, func(t *testing.T) {
			l := algorithm.build(client)
			rule := Rule{Limit: 100, Window: time.Hour}

			allowed := allowConcurrently(t, l, uniqueKey(t), rule, 100, 20)

			if allowed != 100 {
				t.Fatalf("allowed %d of 2000 concurrent requests, want exactly 100", allowed)
			}
		})
	}
}

func TestSlidingLogStillDeniesHalfAWindowLater(t *testing.T) {
	l := NewRedisSlidingLog(newTestRedis(t))
	rule := Rule{Limit: 5, Window: 400 * time.Millisecond}
	key := uniqueKey(t)

	allowN(t, l, key, rule, 5)
	time.Sleep(rule.Window / 2)
	if allowN(t, l, key, rule, 1) != 0 {
		t.Fatalf("sliding log allowed a request inside the rolling window")
	}

	time.Sleep(rule.Window/2 + 50*time.Millisecond)
	if allowN(t, l, key, rule, 10) != 5 {
		t.Fatalf("sliding log did not free the window after it rolled past")
	}
}

func TestSlidingLogRetryAfterIsTimeUntilOldestExpires(t *testing.T) {
	l := NewRedisSlidingLog(newTestRedis(t))
	rule := Rule{Limit: 1, Window: 2 * time.Second}
	key := uniqueKey(t)

	allowN(t, l, key, rule, 1)
	time.Sleep(500 * time.Millisecond)
	decision, _ := l.Allow(context.Background(), single(key, rule))

	want := 1500 * time.Millisecond
	tolerance := 150 * time.Millisecond
	if decision.RetryAfter < want-tolerance || decision.RetryAfter > want+tolerance {
		t.Fatalf("retry after %v, want about %v", decision.RetryAfter, want)
	}
}

func allowChecks(t *testing.T, l Limiter, checks []Check, n int) int {
	t.Helper()
	allowed := 0
	for range n {
		decision, err := l.Allow(context.Background(), checks)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if decision.Allowed {
			allowed++
		}
	}
	return allowed
}

func TestRedisMultiCheckIsAllOrNothing(t *testing.T) {
	client := newTestRedis(t)
	for _, algorithm := range redisAlgorithms() {
		t.Run(algorithm.name, func(t *testing.T) {
			l := algorithm.build(client)
			wide := Check{Key: uniqueKey(t) + "-wide", Rule: Rule{Limit: 5, Window: time.Hour}}
			narrow := Check{Key: uniqueKey(t) + "-narrow", Rule: Rule{Limit: 3, Window: time.Hour}}

			allowedTogether := allowChecks(t, l, []Check{wide, narrow}, 10)
			allowedWideAlone := allowChecks(t, l, []Check{wide}, 10)

			if allowedTogether != 3 {
				t.Fatalf("allowed %d together, want 3", allowedTogether)
			}
			if allowedWideAlone != 2 {
				t.Fatalf("wide allowed %d alone afterwards, want 2: denied requests must not consume it", allowedWideAlone)
			}
		})
	}
}

func TestRedisMultiCheckReportsWhichCheck(t *testing.T) {
	client := newTestRedis(t)
	for _, algorithm := range redisAlgorithms() {
		t.Run(algorithm.name, func(t *testing.T) {
			l := algorithm.build(client)
			wide := Check{Key: uniqueKey(t) + "-wide", Rule: Rule{Limit: 100, Window: time.Hour}}
			narrow := Check{Key: uniqueKey(t) + "-narrow", Rule: Rule{Limit: 1, Window: time.Hour}}
			checks := []Check{wide, narrow}

			first, err := l.Allow(context.Background(), checks)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			second, _ := l.Allow(context.Background(), checks)

			if !first.Allowed || first.CheckIndex != 1 || first.Remaining != 0 {
				t.Fatalf("first: %+v, want allowed with tightest check 1 and 0 remaining", first)
			}
			if second.Allowed || second.CheckIndex != 1 {
				t.Fatalf("second: %+v, want denied by check 1", second)
			}
		})
	}
}

func TestRedisMultiCheckSharedLimitIsAtomic(t *testing.T) {
	client := newTestRedis(t)
	for _, algorithm := range redisAlgorithms() {
		t.Run(algorithm.name, func(t *testing.T) {
			l := algorithm.build(client)
			shared := Check{Key: uniqueKey(t) + "-shared", Rule: Rule{Limit: 150, Window: time.Hour}}
			keyA := Check{Key: uniqueKey(t) + "-a", Rule: Rule{Limit: 100, Window: time.Hour}}
			keyB := Check{Key: uniqueKey(t) + "-b", Rule: Rule{Limit: 100, Window: time.Hour}}

			var allowedA atomic.Int64
			var allowedB atomic.Int64
			var wg sync.WaitGroup
			for worker := range 100 {
				wg.Go(func() {
					checks := []Check{keyA, shared}
					counter := &allowedA
					if worker%2 == 1 {
						checks = []Check{keyB, shared}
						counter = &allowedB
					}
					counter.Add(int64(allowChecks(t, l, checks, 20)))
				})
			}
			wg.Wait()

			total := allowedA.Load() + allowedB.Load()
			if total != 150 {
				t.Fatalf("shared limit allowed %d, want exactly 150", total)
			}
			if allowedA.Load() > 100 || allowedB.Load() > 100 {
				t.Fatalf("per-key limit broken: a=%d b=%d", allowedA.Load(), allowedB.Load())
			}
		})
	}
}
