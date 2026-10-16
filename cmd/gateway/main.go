package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/parthparmar-2006/headgate/internal/gateway"
	"github.com/parthparmar-2006/headgate/internal/limiter"
	"github.com/redis/go-redis/v9"
)

type config struct {
	listenAddr  string
	upstreamURL *url.URL
	rule        limiter.Rule
	limiterKind string
	algorithm   string
	redisAddr   string
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	l, err := buildLimiter(cfg)
	if err != nil {
		log.Fatalf("limiter: %v", err)
	}
	handler := gateway.NewHandler(l, cfg.rule, cfg.upstreamURL)

	server := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("gateway listening on %s, upstream %s, limiter %s, algorithm %s, limit %d per %s",
		cfg.listenAddr, cfg.upstreamURL, cfg.limiterKind, cfg.algorithm, cfg.rule.Limit, cfg.rule.Window)
	log.Fatal(server.ListenAndServe())
}

func buildLimiter(cfg config) (limiter.Limiter, error) {
	if cfg.limiterKind == "memory" {
		return buildMemoryLimiter(cfg.algorithm)
	}
	if cfg.limiterKind != "redis" {
		return nil, fmt.Errorf("unknown limiter %q", cfg.limiterKind)
	}

	client, err := connectRedis(cfg.redisAddr)
	if err != nil {
		return nil, err
	}
	return buildRedisLimiter(cfg.algorithm, client)
}

func buildMemoryLimiter(algorithm string) (limiter.Limiter, error) {
	if algorithm != "fixed_window" {
		return nil, fmt.Errorf("memory limiter only supports fixed_window, got %q", algorithm)
	}
	return limiter.NewMemoryFixedWindow(limiter.SystemClock{}), nil
}

func buildRedisLimiter(algorithm string, client *redis.Client) (limiter.Limiter, error) {
	switch algorithm {
	case "fixed_window":
		return limiter.NewRedisFixedWindow(client), nil
	case "sliding_log":
		return limiter.NewRedisSlidingLog(client), nil
	case "sliding_counter":
		return limiter.NewRedisSlidingCounter(client), nil
	}
	return nil, fmt.Errorf("unknown algorithm %q", algorithm)
}

func connectRedis(addr string) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{Addr: addr})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.Ping(ctx).Err()
	if err != nil {
		return nil, fmt.Errorf("redis at %s: %w", addr, err)
	}
	return client, nil
}

func loadConfig() (config, error) {
	upstream, err := url.Parse(envOrDefault("UPSTREAM_URL", "http://localhost:9000"))
	if err != nil {
		return config{}, err
	}

	limit, err := strconv.Atoi(envOrDefault("RATE_LIMIT", "100"))
	if err != nil {
		return config{}, err
	}

	window, err := time.ParseDuration(envOrDefault("RATE_WINDOW", "1s"))
	if err != nil {
		return config{}, err
	}

	cfg := config{
		listenAddr:  envOrDefault("LISTEN_ADDR", ":8080"),
		upstreamURL: upstream,
		rule:        limiter.Rule{Limit: limit, Window: window},
		limiterKind: envOrDefault("LIMITER", "redis"),
		algorithm:   envOrDefault("ALGORITHM", "sliding_log"),
		redisAddr:   envOrDefault("REDIS_ADDR", "localhost:6379"),
	}
	return cfg, nil
}

func envOrDefault(name string, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}
