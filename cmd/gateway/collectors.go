package main

import (
	"github.com/parthparmar-2006/headgate/internal/limiter"
	"github.com/parthparmar-2006/headgate/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

func registerDenyCacheMetrics(m *metrics.Metrics, routeName string, cache *limiter.DenyCache) {
	labels := prometheus.Labels{"route": routeName}

	m.Register(prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name:        "headgate_deny_cache_hits_total",
		Help:        "Denials answered from the local deny cache without calling Redis.",
		ConstLabels: labels,
	}, func() float64 {
		return float64(cache.Hits())
	}))

	m.Register(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name:        "headgate_deny_cache_entries",
		Help:        "Keys currently held in the local deny cache.",
		ConstLabels: labels,
	}, func() float64 {
		return float64(cache.Size())
	}))
}

func registerRedisPoolMetrics(m *metrics.Metrics, client *redis.Client) {
	m.Register(prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "headgate_redis_pool_misses_total",
		Help: "Times a Redis connection had to be opened because none was idle.",
	}, func() float64 {
		return float64(client.PoolStats().Misses)
	}))

	m.Register(prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "headgate_redis_pool_timeouts_total",
		Help: "Times waiting for a Redis connection from the pool timed out.",
	}, func() float64 {
		return float64(client.PoolStats().Timeouts)
	}))

	m.Register(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "headgate_redis_pool_connections",
		Help: "Open Redis connections in the pool.",
	}, func() float64 {
		return float64(client.PoolStats().TotalConns)
	}))
}
