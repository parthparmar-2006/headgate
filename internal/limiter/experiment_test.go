package limiter

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parthparmar-2006/headgate/internal/rolling"
	"github.com/redis/go-redis/v9"
)

func requireExperiments(t *testing.T) {
	t.Helper()
	if os.Getenv("HEADGATE_EXPERIMENTS") == "" {
		t.Skip("HEADGATE_EXPERIMENTS not set")
	}
}

func warmConnections(t *testing.T, client *redis.Client, connections int) {
	t.Helper()
	var wg sync.WaitGroup
	for range connections {
		wg.Go(func() {
			client.Ping(context.Background())
		})
	}
	wg.Wait()
}

func redisNowMs(t *testing.T, client *redis.Client) int64 {
	t.Helper()
	now, err := client.Time(context.Background()).Result()
	if err != nil {
		t.Fatalf("redis TIME: %v", err)
	}
	return now.UnixMilli()
}

func sleepUntilWindowPosition(t *testing.T, client *redis.Client, window time.Duration, positionMs int64) {
	t.Helper()
	windowMs := window.Milliseconds()
	offset := redisNowMs(t, client) % windowMs
	waitMs := (positionMs - offset + windowMs) % windowMs
	time.Sleep(time.Duration(waitMs) * time.Millisecond)
}

func TestExperimentEdgeBurst(t *testing.T) {
	requireExperiments(t)
	client := newTestRedis(t)
	rule := Rule{Limit: 100, Window: time.Second}
	trials := 3
	warmConnections(t, client, rule.Limit)

	for _, algorithm := range redisAlgorithms() {
		l := algorithm.build(client)
		for trial := range trials {
			key := uniqueKey(t) + "-" + algorithm.name + "-" + strconv.Itoa(trial)

			sleepUntilWindowPosition(t, client, rule.Window, 900)
			before := allowConcurrently(t, l, key, rule, rule.Limit, 1)
			sleepUntilWindowPosition(t, client, rule.Window, 100)
			after := allowConcurrently(t, l, key, rule, rule.Limit, 1)

			total := before + after
			t.Logf("%-16s trial %d: allowed %3d before edge + %3d after edge = %3d within ~200ms (limit %d per 1s)",
				algorithm.name, trial, before, after, total, rule.Limit)
		}
	}
}

func TestExperimentBurstThenTrickle(t *testing.T) {
	requireExperiments(t)
	client := newTestRedis(t)
	rule := Rule{Limit: 100, Window: time.Second}
	trials := 3
	trickleInterval := 5 * time.Millisecond
	warmConnections(t, client, rule.Limit)

	for _, algorithm := range redisAlgorithms() {
		l := algorithm.build(client)
		for trial := range trials {
			key := uniqueKey(t) + "-" + algorithm.name + "-" + strconv.Itoa(trial)

			sleepUntilWindowPosition(t, client, rule.Window, 950)
			burst := allowedTimestamps(t, l, key, rule, rule.Limit)
			burstEdgeMs := windowEndAfter(burst[0], rule.Window)
			burstBeforeEdge := countBefore(burst, burstEdgeMs)

			trickle, sent := trickleFor(t, l, key, rule, rule.Window, trickleInterval)

			all := append(burst, trickle...)
			worst := rolling.MaxInWindow(all, rule.Window.Milliseconds())
			t.Logf("%-16s trial %d: burst %d (%d before edge), trickle sent %d allowed %d, worst rolling 1s = %3d (limit %d)",
				algorithm.name, trial, len(burst), burstBeforeEdge, sent, len(trickle), worst, rule.Limit)
		}
	}
}

type countingLimiter struct {
	inner Limiter
	calls atomic.Int64
}

func (c *countingLimiter) Allow(ctx context.Context, checks []Check) (Decision, error) {
	c.calls.Add(1)
	return c.inner.Allow(ctx, checks)
}

func TestExperimentDenyCacheAccuracy(t *testing.T) {
	requireExperiments(t)
	client := newTestRedis(t)
	rule := Rule{Limit: 100, Window: time.Second}
	trickleInterval := 2 * time.Millisecond
	warmConnections(t, client, rule.Limit)

	for _, algorithm := range redisAlgorithms() {
		for _, useCache := range []bool{false, true} {
			counter := &countingLimiter{inner: algorithm.build(client)}
			var l Limiter = counter
			if useCache {
				l = NewDenyCache(counter, SystemClock{}, 1000)
			}
			key := uniqueKey(t) + "-" + algorithm.name + "-" + strconv.FormatBool(useCache)

			sleepUntilWindowPosition(t, client, rule.Window, 950)
			burst := allowedTimestamps(t, l, key, rule, rule.Limit)
			trickle, sent := trickleFor(t, l, key, rule, 2*rule.Window, trickleInterval)

			all := append(burst, trickle...)
			worst := rolling.MaxInWindow(all, rule.Window.Milliseconds())
			t.Logf("%-16s cache=%-5t sent %4d, allowed %3d, worst rolling 1s %3d, redis calls %4d",
				algorithm.name, useCache, len(burst)+sent, len(all), worst, counter.calls.Load())
		}
	}
}

func trickleFor(t *testing.T, l Limiter, key string, rule Rule, duration time.Duration, interval time.Duration) ([]int64, int) {
	t.Helper()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	deadline := time.Now().Add(duration)
	timestamps := []int64{}
	sent := 0
	for time.Now().Before(deadline) {
		<-ticker.C
		sent++
		timestamps = append(timestamps, allowedTimestamps(t, l, key, rule, 1)...)
	}
	return timestamps, sent
}

func windowEndAfter(timestampMs int64, window time.Duration) int64 {
	windowMs := window.Milliseconds()
	return timestampMs - (timestampMs % windowMs) + windowMs
}

func countBefore(timestamps []int64, limitMs int64) int {
	count := 0
	for _, timestamp := range timestamps {
		if timestamp < limitMs {
			count++
		}
	}
	return count
}

func allowedTimestamps(t *testing.T, l Limiter, key string, rule Rule, concurrent int) []int64 {
	t.Helper()
	var mu sync.Mutex
	var wg sync.WaitGroup
	timestamps := []int64{}

	for range concurrent {
		wg.Go(func() {
			decision, err := l.Allow(context.Background(), single(key, rule))
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if decision.Allowed {
				mu.Lock()
				timestamps = append(timestamps, decision.DecidedAt.UnixMilli())
				mu.Unlock()
			}
		})
	}

	wg.Wait()
	return timestamps
}

func TestExperimentMemoryPerKey(t *testing.T) {
	requireExperiments(t)
	client := newTestRedis(t)
	limits := []int{10, 100, 1000, 10000}

	for _, algorithm := range redisAlgorithms() {
		l := algorithm.build(client)
		for _, limit := range limits {
			rule := Rule{Limit: limit, Window: time.Hour}
			apiKey := uniqueKey(t) + "-" + algorithm.name + "-" + strconv.Itoa(limit)

			allowed := allowConcurrently(t, l, apiKey, rule, 50, limit/50+1)
			bytes := memoryUsage(t, client, l.RedisKey(apiKey))
			encoding := client.ObjectEncoding(context.Background(), l.RedisKey(apiKey)).Val()

			t.Logf("%-16s limit %5d: %5d stored, %8d bytes, encoding %s",
				algorithm.name, limit, allowed, bytes, encoding)
		}
	}
}

func memoryUsage(t *testing.T, client *redis.Client, key string) int64 {
	t.Helper()
	bytes, err := client.MemoryUsage(context.Background(), key, 0).Result()
	if err != nil {
		t.Fatalf("MEMORY USAGE %s: %v", key, err)
	}
	return bytes
}

func TestExperimentRedisCostPerCall(t *testing.T) {
	requireExperiments(t)
	client := newTestRedis(t)
	ctx := context.Background()
	rule := Rule{Limit: 1000, Window: time.Second}
	rounds := 3
	callsPerRound := 20000

	results := make(map[string][]float64)
	for round := range rounds {
		for _, algorithm := range redisAlgorithms() {
			l := algorithm.build(client)
			key := uniqueKey(t) + "-" + algorithm.name + "-" + strconv.Itoa(round)

			client.ConfigResetStat(ctx)
			allowConcurrently(t, l, key, rule, 50, callsPerRound/50)
			usec := evalshaMicrosPerCall(t, client)
			results[algorithm.name] = append(results[algorithm.name], usec)
		}
	}

	for _, algorithm := range redisAlgorithms() {
		samples := results[algorithm.name]
		sort.Float64s(samples)
		t.Logf("%-16s usec per call inside redis: median %.2f, samples %v",
			algorithm.name, samples[len(samples)/2], samples)
	}
}

func evalshaMicrosPerCall(t *testing.T, client *redis.Client) float64 {
	t.Helper()
	info, err := client.Info(context.Background(), "commandstats").Result()
	if err != nil {
		t.Fatalf("INFO commandstats: %v", err)
	}

	for line := range strings.SplitSeq(info, "\n") {
		if !strings.HasPrefix(line, "cmdstat_evalsha:") {
			continue
		}
		for field := range strings.SplitSeq(strings.TrimSpace(line), ",") {
			valueText, found := strings.CutPrefix(field, "usec_per_call=")
			if found {
				value, _ := strconv.ParseFloat(valueText, 64)
				return value
			}
		}
	}
	t.Fatalf("cmdstat_evalsha not found")
	return 0
}

type checkStrategy struct {
	name   string
	decide func(l Limiter, key Check, tenant Check) bool
}

func allowedBy(l Limiter, checks []Check) bool {
	decision, err := l.Allow(context.Background(), checks)
	return err == nil && decision.Allowed
}

func tenantStrategies() []checkStrategy {
	return []checkStrategy{
		{name: "atomic", decide: func(l Limiter, key Check, tenant Check) bool {
			return allowedBy(l, []Check{key, tenant})
		}},
		{name: "tenant_then_key", decide: func(l Limiter, key Check, tenant Check) bool {
			if !allowedBy(l, []Check{tenant}) {
				return false
			}
			return allowedBy(l, []Check{key})
		}},
		{name: "key_then_tenant", decide: func(l Limiter, key Check, tenant Check) bool {
			if !allowedBy(l, []Check{key}) {
				return false
			}
			return allowedBy(l, []Check{tenant})
		}},
	}
}

func sendAtRate(l Limiter, strategy checkStrategy, key Check, tenant Check, interval time.Duration, duration time.Duration) int64 {
	var allowed atomic.Int64
	var wg sync.WaitGroup
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		<-ticker.C
		wg.Go(func() {
			if strategy.decide(l, key, tenant) {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()
	return allowed.Load()
}

func TestExperimentTenantFairness(t *testing.T) {
	requireExperiments(t)
	client := newTestRedis(t)
	warmConnections(t, client, 100)
	l := NewRedisSlidingLog(client)
	duration := 5 * time.Second

	for _, tenantLimit := range []int{150, 120} {
		for _, strategy := range tenantStrategies() {
			runFairnessTrial(t, l, strategy, tenantLimit, duration)
		}
	}
}

func runFairnessTrial(t *testing.T, l Limiter, strategy checkStrategy, tenantLimit int, duration time.Duration) {
	prefix := uniqueKey(t) + "-" + strategy.name + "-" + strconv.Itoa(tenantLimit)
	tenant := Check{Key: prefix + "-tenant", Rule: Rule{Limit: tenantLimit, Window: time.Second}}
	abusive := Check{Key: prefix + "-a", Rule: Rule{Limit: 100, Window: time.Second}}
	polite := Check{Key: prefix + "-b", Rule: Rule{Limit: 100, Window: time.Second}}

	var abusiveAllowed int64
	var politeAllowed int64
	var wg sync.WaitGroup
	wg.Go(func() {
		abusiveAllowed = sendAtRate(l, strategy, abusive, tenant, 2*time.Millisecond, duration)
	})
	wg.Go(func() {
		politeAllowed = sendAtRate(l, strategy, polite, tenant, 20*time.Millisecond, duration)
	})
	wg.Wait()

	seconds := duration.Seconds()
	t.Logf("tenant limit %d  %-16s abusive A (500/s offered): %5.1f/s   polite B (50/s offered): %5.1f/s   tenant total: %5.1f/s",
		tenantLimit, strategy.name, float64(abusiveAllowed)/seconds, float64(politeAllowed)/seconds,
		float64(abusiveAllowed+politeAllowed)/seconds)
}
