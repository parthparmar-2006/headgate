package gateway

import (
	"context"
	"net/http"
	"time"
)

type requestTiming struct {
	upstream time.Duration
}

type timingKey struct{}

func withTiming(ctx context.Context, timing *requestTiming) context.Context {
	return context.WithValue(ctx, timingKey{}, timing)
}

func timingFrom(ctx context.Context) (*requestTiming, bool) {
	timing, found := ctx.Value(timingKey{}).(*requestTiming)
	return timing, found
}

type timingTransport struct {
	inner http.RoundTripper
}

func (t timingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	start := time.Now()
	response, err := t.inner.RoundTrip(request)

	timing, found := timingFrom(request.Context())
	if found {
		timing.upstream = time.Since(start)
	}
	return response, err
}
