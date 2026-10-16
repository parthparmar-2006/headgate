package limiter

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
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
			decision, err := l.Allow(context.Background(), key, rule)
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
