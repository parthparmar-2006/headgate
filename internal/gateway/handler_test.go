package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/parthparmar-2006/headgate/internal/limiter"
)

type fakeLimiter struct {
	mu       sync.Mutex
	decision limiter.Decision
	err      error
	hang     bool
	keys     []string
}

func (f *fakeLimiter) Allow(ctx context.Context, key string, rule limiter.Rule) (limiter.Decision, error) {
	f.mu.Lock()
	f.keys = append(f.keys, key)
	f.mu.Unlock()

	if f.hang {
		<-ctx.Done()
		return limiter.Decision{}, ctx.Err()
	}
	return f.decision, f.err
}

type recordingUpstream struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []*http.Request
}

func newRecordingUpstream(t *testing.T) *recordingUpstream {
	t.Helper()
	upstream := &recordingUpstream{}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.mu.Lock()
		upstream.requests = append(upstream.requests, r)
		upstream.mu.Unlock()
		w.Write([]byte("upstream"))
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

func (u *recordingUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.requests)
}

func (u *recordingUpstream) last() *http.Request {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.requests[len(u.requests)-1]
}

func newTestHandler(t *testing.T, upstream *recordingUpstream, routes []Route) *Handler {
	t.Helper()
	target, err := url.Parse(upstream.server.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	return NewHandler(NewRouter(routes), target, 50*time.Millisecond)
}

func singleRoute(l limiter.Limiter, failOpen bool) []Route {
	return []Route{{
		Name:       "api",
		PathPrefix: "/",
		Rule:       limiter.Rule{Limit: 10, Window: time.Second},
		Limiter:    l,
		FailOpen:   failOpen,
	}}
}

func send(h http.Handler, path string, apiKey string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if apiKey != "" {
		request.Header.Set(APIKeyHeader, apiKey)
	}
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	return recorder
}

func TestHandlerRejectsMissingAPIKey(t *testing.T) {
	upstream := newRecordingUpstream(t)
	l := &fakeLimiter{decision: limiter.Decision{Allowed: true}}
	h := newTestHandler(t, upstream, singleRoute(l, false))

	response := send(h, "/x", "")

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", response.Code)
	}
	if upstream.count() != 0 {
		t.Fatalf("upstream was called")
	}
}

func TestHandlerReturns404WhenNoRouteMatches(t *testing.T) {
	upstream := newRecordingUpstream(t)
	routes := []Route{{Name: "payments", PathPrefix: "/payments", Limiter: &fakeLimiter{}}}
	h := newTestHandler(t, upstream, routes)

	response := send(h, "/other", "key")

	if response.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", response.Code)
	}
}

func TestHandlerForwardsAllowedRequestWithDecisionTime(t *testing.T) {
	upstream := newRecordingUpstream(t)
	decidedAt := time.UnixMilli(1760000000123)
	l := &fakeLimiter{decision: limiter.Decision{Allowed: true, Remaining: 9, DecidedAt: decidedAt}}
	h := newTestHandler(t, upstream, singleRoute(l, false))

	response := send(h, "/x", "key-a")

	if response.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", response.Code)
	}
	if response.Header().Get("X-RateLimit-Remaining") != "9" {
		t.Fatalf("remaining header %q, want 9", response.Header().Get("X-RateLimit-Remaining"))
	}
	if upstream.last().Header.Get(DecisionTimeHeader) != "1760000000123" {
		t.Fatalf("decision header %q", upstream.last().Header.Get(DecisionTimeHeader))
	}
	if l.keys[0] != "api:key-a" {
		t.Fatalf("limiter key %q, want api:key-a", l.keys[0])
	}
}

func TestHandlerDeniesWith429AndRetryAfter(t *testing.T) {
	upstream := newRecordingUpstream(t)
	l := &fakeLimiter{decision: limiter.Decision{Allowed: false, RetryAfter: 1500 * time.Millisecond}}
	h := newTestHandler(t, upstream, singleRoute(l, false))

	response := send(h, "/x", "key-a")

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", response.Code)
	}
	if response.Header().Get("Retry-After") != "2" {
		t.Fatalf("retry-after %q, want 2", response.Header().Get("Retry-After"))
	}
	if upstream.count() != 0 {
		t.Fatalf("upstream was called for a denied request")
	}
}

func TestHandlerFailOpenForwardsWhenLimiterErrors(t *testing.T) {
	upstream := newRecordingUpstream(t)
	l := &fakeLimiter{err: errors.New("redis down")}
	h := newTestHandler(t, upstream, singleRoute(l, true))

	response := send(h, "/x", "key-a")

	if response.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", response.Code)
	}
	if response.Header().Get(LimiterStatusHeader) != "fail-open" {
		t.Fatalf("limiter header %q, want fail-open", response.Header().Get(LimiterStatusHeader))
	}
	if upstream.count() != 1 {
		t.Fatalf("upstream calls %d, want 1", upstream.count())
	}
}

func TestHandlerFailClosedRejectsWhenLimiterErrors(t *testing.T) {
	upstream := newRecordingUpstream(t)
	l := &fakeLimiter{err: errors.New("redis down")}
	h := newTestHandler(t, upstream, singleRoute(l, false))

	response := send(h, "/x", "key-a")

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", response.Code)
	}
	if response.Header().Get(LimiterStatusHeader) != "fail-closed" {
		t.Fatalf("limiter header %q, want fail-closed", response.Header().Get(LimiterStatusHeader))
	}
	if upstream.count() != 0 {
		t.Fatalf("upstream was called while failing closed")
	}
}

func TestHandlerBoundsLimiterTimeWhenLimiterHangs(t *testing.T) {
	upstream := newRecordingUpstream(t)
	l := &fakeLimiter{hang: true}
	h := newTestHandler(t, upstream, singleRoute(l, true))

	start := time.Now()
	response := send(h, "/x", "key-a")
	elapsed := time.Since(start)

	if response.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 via fail-open", response.Code)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("took %v with a 50ms limiter budget", elapsed)
	}
}

func TestHandlerUsesPerRouteLimiterAndPolicy(t *testing.T) {
	upstream := newRecordingUpstream(t)
	broken := &fakeLimiter{err: errors.New("redis down")}
	routes := []Route{
		{Name: "payments", PathPrefix: "/payments", Limiter: broken, FailOpen: false},
		{Name: "catalog", PathPrefix: "/catalog", Limiter: broken, FailOpen: true},
	}
	h := newTestHandler(t, upstream, routes)

	payments := send(h, "/payments/charge", "key-a")
	catalog := send(h, "/catalog/items", "key-a")

	if payments.Code != http.StatusServiceUnavailable {
		t.Fatalf("payments status %d, want 503", payments.Code)
	}
	if catalog.Code != http.StatusOK {
		t.Fatalf("catalog status %d, want 200", catalog.Code)
	}
}
