package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/parthparmar-2006/headgate/internal/config"
	"github.com/parthparmar-2006/headgate/internal/gateway"
	"github.com/parthparmar-2006/headgate/internal/limiter"
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

	routes, err := buildRoutes(cfg)
	if err != nil {
		log.Fatalf("routes: %v", err)
	}

	handler := gateway.NewHandler(gateway.NewRouter(routes), upstream, limiterTimeout(cfg))
	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("gateway listening on %s, upstream %s, backend %s", cfg.Listen, cfg.Upstream, cfg.Backend)
	for _, route := range cfg.Routes {
		log.Printf("route %s %s: %s %d per %s, %s",
			route.Name, route.PathPrefix, route.Algorithm, route.Limit, route.Window, route.OnRedisError)
	}
	log.Fatal(server.ListenAndServe())
}

func limiterTimeout(cfg config.Config) time.Duration {
	if cfg.Backend == "memory" {
		return time.Second
	}
	return cfg.Redis.Timeout
}

func buildRoutes(cfg config.Config) ([]gateway.Route, error) {
	var client *redis.Client
	if cfg.Backend == "redis" {
		client = limiter.NewRedisClient(cfg.Redis.Addr, cfg.Redis.MinIdleConns)
		warnIfRedisUnreachable(client, cfg.Redis.Addr)
	}

	routes := []gateway.Route{}
	for _, routeConfig := range cfg.Routes {
		l, err := buildLimiter(cfg.Backend, routeConfig.Algorithm, client)
		if err != nil {
			return nil, fmt.Errorf("route %s: %w", routeConfig.Name, err)
		}

		route := gateway.Route{
			Name:       routeConfig.Name,
			PathPrefix: routeConfig.PathPrefix,
			Rule:       limiter.Rule{Limit: routeConfig.Limit, Window: routeConfig.Window},
			Limiter:    l,
			FailOpen:   routeConfig.OnRedisError == config.FailOpen,
		}
		routes = append(routes, route)
	}
	return routes, nil
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
