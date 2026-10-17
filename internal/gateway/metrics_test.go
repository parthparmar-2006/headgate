package gateway

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parthparmar-2006/headgate/internal/limiter"
	"github.com/parthparmar-2006/headgate/internal/metrics"
)

func scrape(t *testing.T, m *metrics.Metrics) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	m.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return recorder.Body.String()
}

func metricValue(t *testing.T, body string, series string) float64 {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		valueText, found := strings.CutPrefix(line, series+" ")
		if !found {
			continue
		}
		value, err := strconv.ParseFloat(valueText, 64)
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		return value
	}
	t.Fatalf("series %s not found in:\n%s", series, body)
	return 0
}

func newMeasuredHandler(t *testing.T, upstreamDelay time.Duration, l limiter.Limiter) (*Handler, *metrics.Metrics) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(upstreamDelay)
		w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	m := metrics.New()
	h := NewHandler(NewRouter(threeScopeRoute(l)), map[string]string{}, target, 50*time.Millisecond, m)
	return h, m
}

func TestMetricsCountOutcomes(t *testing.T) {
	allowing := &fakeLimiter{decision: limiter.Decision{Allowed: true}}
	h, m := newMeasuredHandler(t, 0, allowing)

	send(h, "/x", "key-a")
	send(h, "/x", "key-a")
	send(h, "/x", "")

	body := scrape(t, m)
	if metricValue(t, body, `headgate_requests_total{outcome="allowed",route="api"}`) != 2 {
		t.Fatalf("allowed count wrong:\n%s", body)
	}
	if metricValue(t, body, `headgate_requests_total{outcome="no_key",route="api"}`) != 1 {
		t.Fatalf("no_key count wrong:\n%s", body)
	}
}

func TestMetricsCountDenialsByScope(t *testing.T) {
	denying := &fakeLimiter{decision: limiter.Decision{Allowed: false, CheckIndex: 1, RetryAfter: time.Second}}
	h, m := newMeasuredHandler(t, 0, denying)

	send(h, "/x", "key-a")

	body := scrape(t, m)
	if metricValue(t, body, `headgate_denied_total{route="api",scope="tenant"}`) != 1 {
		t.Fatalf("denied by tenant count wrong:\n%s", body)
	}
}

func TestMetricsOverheadExcludesUpstreamTime(t *testing.T) {
	allowing := &fakeLimiter{decision: limiter.Decision{Allowed: true}}
	h, m := newMeasuredHandler(t, 50*time.Millisecond, allowing)

	send(h, "/x", "key-a")

	body := scrape(t, m)
	upstream := metricValue(t, body, `headgate_upstream_seconds_sum{route="api"}`)
	overhead := metricValue(t, body, `headgate_overhead_seconds_sum{outcome="allowed",route="api"}`)
	if upstream < 0.050 {
		t.Fatalf("upstream time %.4fs, want at least 0.050s", upstream)
	}
	if overhead > 0.020 {
		t.Fatalf("overhead %.4fs includes upstream time", overhead)
	}
}

func TestMetricsFailOpenOverheadIncludesLimiterBudget(t *testing.T) {
	hanging := &fakeLimiter{hang: true}
	upstream := newRecordingUpstream(t)
	target, _ := url.Parse(upstream.server.URL)
	m := metrics.New()
	routes := []Route{{Name: "api", PathPrefix: "/", Limits: threeScopeRoute(hanging)[0].Limits, Limiter: hanging, FailOpen: true}}
	h := NewHandler(NewRouter(routes), map[string]string{}, target, 50*time.Millisecond, m)

	send(h, "/x", "key-a")

	body := scrape(t, m)
	overhead := metricValue(t, body, `headgate_overhead_seconds_sum{outcome="fail_open",route="api"}`)
	if overhead < 0.050 {
		t.Fatalf("fail-open overhead %.4fs, want at least the 50ms budget", overhead)
	}
}
