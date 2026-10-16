package main

import (
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/parthparmar-2006/headgate/internal/gateway"
	"github.com/parthparmar-2006/headgate/internal/limiter"
)

type config struct {
	listenAddr  string
	upstreamURL *url.URL
	rule        limiter.Rule
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	l := limiter.NewMemoryFixedWindow(limiter.SystemClock{})
	handler := gateway.NewHandler(l, cfg.rule, cfg.upstreamURL)

	server := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("gateway listening on %s, upstream %s, limit %d per %s",
		cfg.listenAddr, cfg.upstreamURL, cfg.rule.Limit, cfg.rule.Window)
	log.Fatal(server.ListenAndServe())
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
