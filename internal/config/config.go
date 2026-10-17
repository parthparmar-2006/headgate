package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	FailOpen   = "fail_open"
	FailClosed = "fail_closed"
)

const (
	PerKey    = "key"
	PerTenant = "tenant"
	PerRoute  = "route"
)

const UnassignedTenant = "_unassigned"

type Config struct {
	Listen        string              `yaml:"listen"`
	MetricsListen string              `yaml:"metrics_listen"`
	Upstream      string              `yaml:"upstream"`
	Backend       string              `yaml:"backend"`
	Redis         RedisConfig         `yaml:"redis"`
	DenyCache     DenyCacheConfig     `yaml:"deny_cache"`
	Tenants       map[string][]string `yaml:"tenants"`
	Routes        []RouteConfig       `yaml:"routes"`
}

type DenyCacheConfig struct {
	Enabled bool `yaml:"enabled"`
	MaxKeys int  `yaml:"max_keys"`
}

type RedisConfig struct {
	Addr         string        `yaml:"addr"`
	Timeout      time.Duration `yaml:"timeout"`
	MinIdleConns int           `yaml:"min_idle_conns"`
}

type RouteConfig struct {
	Name         string        `yaml:"name"`
	PathPrefix   string        `yaml:"path_prefix"`
	Algorithm    string        `yaml:"algorithm"`
	OnRedisError string        `yaml:"on_redis_error"`
	Limits       []LimitConfig `yaml:"limits"`
}

type LimitConfig struct {
	Per    string        `yaml:"per"`
	Limit  int           `yaml:"limit"`
	Window time.Duration `yaml:"window"`
}

func (cfg Config) TenantByKey() map[string]string {
	tenantByKey := make(map[string]string)
	for tenant, keys := range cfg.Tenants {
		for _, key := range keys {
			tenantByKey[key] = tenant
		}
	}
	return tenantByKey
}

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Parse(raw)
}

func Parse(raw []byte) (Config, error) {
	expanded := os.ExpandEnv(string(raw))

	decoder := yaml.NewDecoder(bytes.NewReader([]byte(expanded)))
	decoder.KnownFields(true)

	var cfg Config
	err := decoder.Decode(&cfg)
	if err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	err = validate(cfg)
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validate(cfg Config) error {
	if cfg.Listen == "" {
		return fmt.Errorf("listen is required")
	}
	if cfg.Upstream == "" {
		return fmt.Errorf("upstream is required")
	}
	if cfg.Backend != "redis" && cfg.Backend != "memory" {
		return fmt.Errorf("backend must be redis or memory, got %q", cfg.Backend)
	}
	if cfg.Backend == "redis" {
		err := validateRedis(cfg.Redis)
		if err != nil {
			return err
		}
	}
	if cfg.DenyCache.Enabled && cfg.DenyCache.MaxKeys <= 0 {
		return fmt.Errorf("deny_cache.max_keys must be positive when enabled")
	}
	err := validateTenants(cfg.Tenants)
	if err != nil {
		return err
	}
	if len(cfg.Routes) == 0 {
		return fmt.Errorf("at least one route is required")
	}
	return validateRoutes(cfg.Routes, cfg.Backend)
}

func validateTenants(tenants map[string][]string) error {
	owner := make(map[string]string)
	for tenant, keys := range tenants {
		if tenant == "" || tenant == UnassignedTenant {
			return fmt.Errorf("tenant name %q is not allowed", tenant)
		}
		for _, key := range keys {
			if key == "" {
				return fmt.Errorf("tenant %q has an empty api key", tenant)
			}
			previous, taken := owner[key]
			if taken {
				return fmt.Errorf("api key %q belongs to both %q and %q", key, previous, tenant)
			}
			owner[key] = tenant
		}
	}
	return nil
}

func validateRedis(redis RedisConfig) error {
	if redis.Addr == "" {
		return fmt.Errorf("redis.addr is required")
	}
	if redis.Timeout <= 0 {
		return fmt.Errorf("redis.timeout must be positive")
	}
	if redis.MinIdleConns < 0 {
		return fmt.Errorf("redis.min_idle_conns must not be negative")
	}
	return nil
}

func validateRoutes(routes []RouteConfig, backend string) error {
	seenNames := make(map[string]bool)
	seenPrefixes := make(map[string]bool)

	for _, route := range routes {
		err := validateRoute(route, backend)
		if err != nil {
			return fmt.Errorf("route %q: %w", route.Name, err)
		}
		if seenNames[route.Name] {
			return fmt.Errorf("duplicate route name %q", route.Name)
		}
		if seenPrefixes[route.PathPrefix] {
			return fmt.Errorf("duplicate path_prefix %q", route.PathPrefix)
		}
		seenNames[route.Name] = true
		seenPrefixes[route.PathPrefix] = true
	}
	return nil
}

func validateRoute(route RouteConfig, backend string) error {
	if route.Name == "" {
		return fmt.Errorf("name is required")
	}
	if !strings.HasPrefix(route.PathPrefix, "/") {
		return fmt.Errorf("path_prefix must start with /")
	}
	if !isKnownAlgorithm(route.Algorithm) {
		return fmt.Errorf("unknown algorithm %q", route.Algorithm)
	}
	if backend == "memory" && route.Algorithm != "fixed_window" {
		return fmt.Errorf("memory backend only supports fixed_window")
	}
	if route.OnRedisError != FailOpen && route.OnRedisError != FailClosed {
		return fmt.Errorf("on_redis_error must be %s or %s", FailOpen, FailClosed)
	}
	return validateLimits(route.Limits)
}

func validateLimits(limits []LimitConfig) error {
	if len(limits) == 0 {
		return fmt.Errorf("at least one limit is required")
	}

	seenScopes := make(map[string]bool)
	for _, limit := range limits {
		if limit.Per != PerKey && limit.Per != PerTenant && limit.Per != PerRoute {
			return fmt.Errorf("limit per must be %s, %s or %s, got %q", PerKey, PerTenant, PerRoute, limit.Per)
		}
		if seenScopes[limit.Per] {
			return fmt.Errorf("duplicate limit per %s", limit.Per)
		}
		seenScopes[limit.Per] = true

		if limit.Limit <= 0 {
			return fmt.Errorf("limit must be positive")
		}
		if limit.Window < time.Millisecond {
			return fmt.Errorf("window must be at least 1ms")
		}
	}
	return nil
}

func isKnownAlgorithm(name string) bool {
	return name == "fixed_window" || name == "sliding_log" || name == "sliding_counter"
}
