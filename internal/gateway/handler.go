package gateway

import (
	"context"
	"math"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"time"

	"github.com/parthparmar-2006/headgate/internal/limiter"
	"github.com/parthparmar-2006/headgate/internal/metrics"
)

const APIKeyHeader = "X-API-Key"

const TenantHeader = "X-Headgate-Tenant"

const DecisionTimeHeader = "X-Headgate-Decision-Ms"

const LimiterStatusHeader = "X-Headgate-Limiter"

const UnassignedTenant = "_unassigned"

type Handler struct {
	router         *Router
	tenantByKey    map[string]string
	proxy          *httputil.ReverseProxy
	limiterTimeout time.Duration
	metrics        *metrics.Metrics
}

func NewHandler(router *Router, tenantByKey map[string]string, upstream *url.URL, limiterTimeout time.Duration, m *metrics.Metrics) *Handler {
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.Transport = timingTransport{inner: newUpstreamTransport()}

	return &Handler{
		router:         router,
		tenantByKey:    tenantByKey,
		proxy:          proxy,
		limiterTimeout: limiterTimeout,
		metrics:        m,
	}
}

func newUpstreamTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 1024
	transport.MaxIdleConnsPerHost = 1024
	transport.IdleConnTimeout = 90 * time.Second
	return transport
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	route, found := h.router.Match(r.URL.Path)
	if !found {
		h.metrics.CountRequest("", metrics.OutcomeNoRoute)
		http.NotFound(w, r)
		return
	}

	apiKey := r.Header.Get(APIKeyHeader)
	if apiKey == "" {
		h.metrics.CountRequest(route.Name, metrics.OutcomeNoKey)
		http.Error(w, "missing API key", http.StatusUnauthorized)
		return
	}
	tenant := h.tenantOf(apiKey)
	r.Header.Set(TenantHeader, tenant)
	r.Header.Del(DecisionTimeHeader)

	decision, err := h.decide(r.Context(), route, apiKey, tenant)
	if err != nil {
		h.handleLimiterError(w, r, route, start)
		return
	}

	writeRateLimitHeaders(w, route, decision)
	if !decision.Allowed {
		h.metrics.CountRequest(route.Name, metrics.OutcomeDenied)
		h.metrics.CountDenied(route.Name, scopeOf(route, decision))
		writeTooManyRequests(w, decision)
		return
	}

	r.Header.Set(DecisionTimeHeader, strconv.FormatInt(decision.DecidedAt.UnixMilli(), 10))
	h.metrics.CountRequest(route.Name, metrics.OutcomeAllowed)
	h.forward(w, r, route, start, metrics.OutcomeAllowed)
}

func (h *Handler) forward(w http.ResponseWriter, r *http.Request, route Route, start time.Time, outcome string) {
	timing := &requestTiming{}
	h.proxy.ServeHTTP(w, r.WithContext(withTiming(r.Context(), timing)))

	total := time.Since(start)
	h.metrics.ObserveUpstream(route.Name, timing.upstream)
	h.metrics.ObserveOverhead(route.Name, outcome, total-timing.upstream)
}

func scopeOf(route Route, decision limiter.Decision) string {
	if decision.CheckIndex < 0 || decision.CheckIndex >= len(route.Limits) {
		return "unknown"
	}
	return route.Limits[decision.CheckIndex].Scope
}

func (h *Handler) tenantOf(apiKey string) string {
	tenant, found := h.tenantByKey[apiKey]
	if !found {
		return UnassignedTenant
	}
	return tenant
}

func (h *Handler) decide(ctx context.Context, route Route, apiKey string, tenant string) (limiter.Decision, error) {
	ctx, cancel := context.WithTimeout(ctx, h.limiterTimeout)
	defer cancel()

	start := time.Now()
	decision, err := route.Limiter.Allow(ctx, checksFor(route, apiKey, tenant))
	h.metrics.ObserveLimiter(route.Name, time.Since(start))
	return decision, err
}

func checksFor(route Route, apiKey string, tenant string) []limiter.Check {
	checks := make([]limiter.Check, len(route.Limits))
	for i, limit := range route.Limits {
		checks[i] = limiter.Check{
			Key:  limiterKey(route.Name, limit.Scope, apiKey, tenant),
			Rule: limit.Rule,
		}
	}
	return checks
}

func limiterKey(routeName string, scope string, apiKey string, tenant string) string {
	switch scope {
	case ScopeKey:
		return routeName + ":key:" + apiKey
	case ScopeTenant:
		return routeName + ":tenant:" + tenant
	}
	return routeName + ":route"
}

func (h *Handler) handleLimiterError(w http.ResponseWriter, r *http.Request, route Route, start time.Time) {
	if route.FailOpen {
		w.Header().Set(LimiterStatusHeader, "fail-open")
		h.metrics.CountRequest(route.Name, metrics.OutcomeFailOpen)
		h.forward(w, r, route, start, metrics.OutcomeFailOpen)
		return
	}

	h.metrics.CountRequest(route.Name, metrics.OutcomeFailClosed)
	w.Header().Set(LimiterStatusHeader, "fail-closed")
	w.Header().Set("Retry-After", "1")
	http.Error(w, "rate limiter unavailable", http.StatusServiceUnavailable)
}

func writeRateLimitHeaders(w http.ResponseWriter, route Route, decision limiter.Decision) {
	if decision.CheckIndex < 0 || decision.CheckIndex >= len(route.Limits) {
		return
	}
	limit := route.Limits[decision.CheckIndex]
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit.Rule.Limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(decision.Remaining))
	w.Header().Set("X-RateLimit-Scope", limit.Scope)
}

func writeTooManyRequests(w http.ResponseWriter, decision limiter.Decision) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(decision.RetryAfter)))
	http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
}

func retryAfterSeconds(d time.Duration) int {
	seconds := int(math.Ceil(d.Seconds()))
	if seconds < 1 {
		return 1
	}
	return seconds
}
