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
}

func NewHandler(router *Router, tenantByKey map[string]string, upstream *url.URL, limiterTimeout time.Duration) *Handler {
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.Transport = newUpstreamTransport()

	return &Handler{
		router:         router,
		tenantByKey:    tenantByKey,
		proxy:          proxy,
		limiterTimeout: limiterTimeout,
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
	route, found := h.router.Match(r.URL.Path)
	if !found {
		http.NotFound(w, r)
		return
	}

	apiKey := r.Header.Get(APIKeyHeader)
	if apiKey == "" {
		http.Error(w, "missing API key", http.StatusUnauthorized)
		return
	}
	tenant := h.tenantOf(apiKey)
	r.Header.Set(TenantHeader, tenant)
	r.Header.Del(DecisionTimeHeader)

	decision, err := h.decide(r.Context(), route, apiKey, tenant)
	if err != nil {
		h.handleLimiterError(w, r, route)
		return
	}

	writeRateLimitHeaders(w, route, decision)
	if !decision.Allowed {
		writeTooManyRequests(w, decision)
		return
	}

	r.Header.Set(DecisionTimeHeader, strconv.FormatInt(decision.DecidedAt.UnixMilli(), 10))
	h.proxy.ServeHTTP(w, r)
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

	return route.Limiter.Allow(ctx, checksFor(route, apiKey, tenant))
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

func (h *Handler) handleLimiterError(w http.ResponseWriter, r *http.Request, route Route) {
	if route.FailOpen {
		w.Header().Set(LimiterStatusHeader, "fail-open")
		h.proxy.ServeHTTP(w, r)
		return
	}

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
