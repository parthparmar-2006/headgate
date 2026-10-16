package gateway

import (
	"math"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"time"

	"github.com/parthparmar-2006/headgate/internal/limiter"
)

const APIKeyHeader = "X-API-Key"

type Handler struct {
	limiter limiter.Limiter
	rule    limiter.Rule
	proxy   *httputil.ReverseProxy
}

func NewHandler(l limiter.Limiter, rule limiter.Rule, upstream *url.URL) *Handler {
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.Transport = newUpstreamTransport()

	return &Handler{
		limiter: l,
		rule:    rule,
		proxy:   proxy,
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
	apiKey := r.Header.Get(APIKeyHeader)
	if apiKey == "" {
		http.Error(w, "missing API key", http.StatusUnauthorized)
		return
	}

	decision, err := h.limiter.Allow(r.Context(), apiKey, h.rule)
	if err != nil {
		http.Error(w, "rate limiter unavailable", http.StatusServiceUnavailable)
		return
	}

	writeRateLimitHeaders(w, h.rule, decision)
	if !decision.Allowed {
		writeTooManyRequests(w, decision)
		return
	}

	h.proxy.ServeHTTP(w, r)
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
