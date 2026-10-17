package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	OutcomeAllowed    = "allowed"
	OutcomeDenied     = "denied"
	OutcomeFailOpen   = "fail_open"
	OutcomeFailClosed = "fail_closed"
	OutcomeNoRoute    = "no_route"
	OutcomeNoKey      = "no_key"
)

type Metrics struct {
	registry *prometheus.Registry
	requests *prometheus.CounterVec
	denied   *prometheus.CounterVec
	limiter  *prometheus.HistogramVec
	upstream *prometheus.HistogramVec
	overhead *prometheus.HistogramVec
}

func latencyBuckets() []float64 {
	return prometheus.ExponentialBuckets(0.00005, 1.25, 45)
}

func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "headgate_requests_total",
			Help: "Requests by route and outcome.",
		}, []string{"route", "outcome"}),
		denied: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "headgate_denied_total",
			Help: "Denied requests by route and the scope of the limit that denied them.",
		}, []string{"route", "scope"}),
		limiter: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "headgate_limiter_seconds",
			Help:    "Time to reach a rate limit decision, including errors and timeouts.",
			Buckets: latencyBuckets(),
		}, []string{"route"}),
		upstream: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "headgate_upstream_seconds",
			Help:    "Upstream round trip until response headers.",
			Buckets: latencyBuckets(),
		}, []string{"route"}),
		overhead: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "headgate_overhead_seconds",
			Help:    "Time the gateway adds to a forwarded request: total handler time minus upstream round trip.",
			Buckets: latencyBuckets(),
		}, []string{"route", "outcome"}),
	}

	m.registry.MustRegister(m.requests, m.denied, m.limiter, m.upstream, m.overhead)
	return m
}

func (m *Metrics) Register(collector prometheus.Collector) {
	m.registry.MustRegister(collector)
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func (m *Metrics) CountRequest(route string, outcome string) {
	m.requests.WithLabelValues(route, outcome).Inc()
}

func (m *Metrics) CountDenied(route string, scope string) {
	m.denied.WithLabelValues(route, scope).Inc()
}

func (m *Metrics) ObserveLimiter(route string, duration time.Duration) {
	m.limiter.WithLabelValues(route).Observe(duration.Seconds())
}

func (m *Metrics) ObserveUpstream(route string, duration time.Duration) {
	m.upstream.WithLabelValues(route).Observe(duration.Seconds())
}

func (m *Metrics) ObserveOverhead(route string, outcome string, duration time.Duration) {
	m.overhead.WithLabelValues(route, outcome).Observe(duration.Seconds())
}
