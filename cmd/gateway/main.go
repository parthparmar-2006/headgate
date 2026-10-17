package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"time"

	"github.com/parthparmar-2006/headgate/internal/config"
	"github.com/parthparmar-2006/headgate/internal/gateway"
	"github.com/parthparmar-2006/headgate/internal/limiter"
	"github.com/parthparmar-2006/headgate/internal/metrics"
	"github.com/redis/go-redis/v9"
)

func main() {
	configPath := os.Getenv("CONFIG_FILE")
	if configPath == "" {
		configPath = "headgate.yaml"
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	upstream, err := url.Parse(cfg.Upstream)
	if err != nil {
		log.Fatalf("upstream url: %v", err)
	}

	m := metrics.New()
	routes, err := buildRoutes(cfg, m)
	if err != nil {
		log.Fatalf("routes: %v", err)
	}

	handler := gateway.NewHandler(gateway.NewRouter(routes), cfg.TenantByKey(), upstream, limiterTimeout(cfg), m)
	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	logStartup(cfg)
	startMetricsServer(cfg.MetricsListen, m)
	log.Fatal(server.ListenAndServe())
}

func startMetricsServer(addr string, m *metrics.Metrics) {
	if addr == "" {
		return
	}

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", m.Handler())
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("metrics listening on %s", addr)
		err := server.ListenAndServe()
		log.Printf("metrics server stopped: %v", err)
	}()
}

func logStartup(cfg config.Config) {
	log.Printf("gateway listening on %s, upstream %s, backend %s, deny cache %t, %d tenants",
		cfg.Listen, cfg.Upstream, cfg.Backend, cfg.DenyCache.Enabled, len(cfg.Tenants))
	for _, route := range cfg.Routes {
		log.Printf("route %s %s: %s, %s", route.Name, route.PathPrefix, route.Algorithm, route.OnRedisError)
		for _, limit := range route.Limits {
			log.Printf("  per %s: %d per %s", limit.Per, limit.Limit, limit.Window)
		}
	}
}

func limiterTimeout(cfg config.Config) time.Duration {
	if cfg.Backend == "memory" {
		return time.Second
	}
	return cfg.Redis.Timeout
}

func buildRoutes(cfg config.Config, m *metrics.Metrics) ([]gateway.Route, error) {
	var client *redis.Client
	if cfg.Backend == "redis" {
		client = limiter.NewRedisClient(cfg.Redis.Addr, cfg.Redis.MinIdleConns)
		warnIfRedisUnreachable(client, cfg.Redis.Addr)
		registerRedisPoolMetrics(m, client)
	}

	routes := []gateway.Route{}
	for _, routeConfig := range cfg.Routes {
		l, err := buildLimiter(cfg.Backend, routeConfig.Algorithm, client)
		if err != nil {
			return nil, fmt.Errorf("route %s: %w", routeConfig.Name, err)
		}
		if cfg.DenyCache.Enabled {
			cache := limiter.NewDenyCache(l, limiter.SystemClock{}, cfg.DenyCache.MaxKeys)
			registerDenyCacheMetrics(m, routeConfig.Name, cache)
			l = cache
		}

		route := gateway.Route{
			Name:       routeConfig.Name,
			PathPrefix: routeConfig.PathPrefix,
			Limits:     routeLimits(routeConfig.Limits),
			Limiter:    l,
			FailOpen:   routeConfig.OnRedisError == config.FailOpen,
		}
		routes = append(routes, route)
	}
	return routes, nil
}

func routeLimits(limitConfigs []config.LimitConfig) []gateway.RouteLimit {
	limits := make([]gateway.RouteLimit, len(limitConfigs))
	for i, limitConfig := range limitConfigs {
		limits[i] = gateway.RouteLimit{
			Scope: limitConfig.Per,
			Rule:  limiter.Rule{Limit: limitConfig.Limit, Window: limitConfig.Window},
		}
	}
	return limits
}

func buildLimiter(backend string, algorithm string, client *redis.Client) (limiter.Limiter, error) {
	if backend == "memory" {
		return limiter.NewMemoryFixedWindow(limiter.SystemClock{}), nil
	}

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

func warnIfRedisUnreachable(client *redis.Client, addr string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := client.Ping(ctx).Err()
	if err != nil {
		log.Printf("warning: redis at %s unreachable at startup, routes will use their failure policy: %v", addr, err)
	}
}
