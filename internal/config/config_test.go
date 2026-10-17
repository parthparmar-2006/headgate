package config

import (
	"strings"
	"testing"
	"time"
)

const validConfig = `
listen: ":8080"
upstream: "http://upstream:9000"
backend: redis
redis:
  addr: "redis:6379"
  timeout: 50ms
routes:
  - name: payments
    path_prefix: /payments
    algorithm: sliding_log
    limit: 100
    window: 1s
    on_redis_error: fail_closed
  - name: catalog
    path_prefix: /catalog
    algorithm: sliding_counter
    limit: 1000
    window: 1m
    on_redis_error: fail_open
`

func TestParseValidConfig(t *testing.T) {
	cfg, err := Parse([]byte(validConfig))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Redis.Timeout != 50*time.Millisecond {
		t.Fatalf("redis timeout %v, want 50ms", cfg.Redis.Timeout)
	}
	if len(cfg.Routes) != 2 {
		t.Fatalf("got %d routes, want 2", len(cfg.Routes))
	}
	catalog := cfg.Routes[1]
	if catalog.Window != time.Minute || catalog.Limit != 1000 || catalog.OnRedisError != FailOpen {
		t.Fatalf("catalog route parsed wrong: %+v", catalog)
	}
}

func TestParseExpandsEnvironmentVariables(t *testing.T) {
	t.Setenv("TEST_ALGORITHM", "fixed_window")
	raw := strings.Replace(validConfig, "algorithm: sliding_log", "algorithm: ${TEST_ALGORITHM}", 1)

	cfg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Routes[0].Algorithm != "fixed_window" {
		t.Fatalf("algorithm %q, want fixed_window", cfg.Routes[0].Algorithm)
	}
}

func TestParseRejectsInvalidConfigs(t *testing.T) {
	cases := []struct {
		name    string
		old     string
		new     string
		wantErr string
	}{
		{"unknown field", "limit: 100", "limt: 100", "field limt not found"},
		{"unknown algorithm", "algorithm: sliding_log", "algorithm: token_bucket", "unknown algorithm"},
		{"zero limit", "limit: 100", "limit: 0", "limit must be positive"},
		{"bad policy", "on_redis_error: fail_closed", "on_redis_error: maybe", "on_redis_error must be"},
		{"prefix without slash", "path_prefix: /payments", "path_prefix: payments", "must start with /"},
		{"duplicate prefix", "path_prefix: /catalog", "path_prefix: /payments", "duplicate path_prefix"},
		{"missing timeout", "timeout: 50ms", "timeout: 0s", "redis.timeout must be positive"},
		{"bad backend", "backend: redis", "backend: memcached", "backend must be"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := strings.Replace(validConfig, c.old, c.new, 1)

			_, err := Parse([]byte(raw))

			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), c.wantErr)
			}
		})
	}
}

func TestParseDenyCache(t *testing.T) {
	enabled := strings.Replace(validConfig, "routes:", "deny_cache:\n  enabled: true\n  max_keys: 5000\nroutes:", 1)
	missingSize := strings.Replace(validConfig, "routes:", "deny_cache:\n  enabled: true\nroutes:", 1)

	cfg, err := Parse([]byte(enabled))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.DenyCache.Enabled || cfg.DenyCache.MaxKeys != 5000 {
		t.Fatalf("deny cache parsed wrong: %+v", cfg.DenyCache)
	}

	_, err = Parse([]byte(missingSize))
	if err == nil || !strings.Contains(err.Error(), "deny_cache.max_keys must be positive") {
		t.Fatalf("expected max_keys error, got %v", err)
	}

	cfg, err = Parse([]byte(validConfig))
	if err != nil || cfg.DenyCache.Enabled {
		t.Fatalf("deny cache should default to disabled, got %+v, %v", cfg.DenyCache, err)
	}
}

func TestParseRejectsMemoryBackendWithSlidingAlgorithm(t *testing.T) {
	raw := strings.Replace(validConfig, "backend: redis", "backend: memory", 1)

	_, err := Parse([]byte(raw))

	if err == nil || !strings.Contains(err.Error(), "memory backend only supports fixed_window") {
		t.Fatalf("expected memory backend error, got %v", err)
	}
}
