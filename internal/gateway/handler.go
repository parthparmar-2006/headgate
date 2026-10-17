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

const DecisionTimeHeader = "X-Headgate-Decision-Ms"

const LimiterStatusHeader = "X-Headgate-Limiter"

type Handler struct {
	router         *Router
	proxy          *httputil.ReverseProxy
	limiterTimeout time.Duration
}

func NewHandler(router *Router, upstream *url.URL, limiterTimeout time.Duration) *Handler {
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.Transport = newUpstreamTransport()

	return &Handler{
		router:         router,
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

	decision, err := h.decide(r.Context(), route, apiKey)
	if err != nil {
		h.handleLimiterError(w, r, route)
		return
	}

	writeRateLimitHeaders(w, route.Rule, decision)
	if !decision.Allowed {
		writeTooManyRequests(w, decision)
		return
	}

	r.Header.Set(DecisionTimeHeader, strconv.FormatInt(decision.DecidedAt.UnixMilli(), 10))
	h.proxy.ServeHTTP(w, r)
}

func (h *Handler) decide(ctx context.Context, route Route, apiKey string) (limiter.Decision, error) {
	ctx, cancel := context.WithTimeout(ctx, h.limiterTimeout)
	defer cancel()

	limiterKey := route.Name + ":" + apiKey
	return route.Limiter.Allow(ctx, limiterKey, route.Rule)
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

func writeRateLimitHeaders(w http.ResponseWriter, rule limiter.Rule, decision limiter.Decision) {
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(rule.Limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(decision.Remaining))
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
