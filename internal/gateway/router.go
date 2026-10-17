package gateway

import (
	"sort"
	"strings"

	"github.com/parthparmar-2006/headgate/internal/limiter"
)

type Route struct {
	Name       string
	PathPrefix string
	Rule       limiter.Rule
	Limiter    limiter.Limiter
	FailOpen   bool
}

type Router struct {
	routes []Route
}

func NewRouter(routes []Route) *Router {
	sorted := make([]Route, len(routes))
	copy(sorted, routes)
	sort.SliceStable(sorted, func(i, j int) bool {
		return len(sorted[i].PathPrefix) > len(sorted[j].PathPrefix)
	})
	return &Router{routes: sorted}
}

func (r *Router) Match(path string) (Route, bool) {
	for _, route := range r.routes {
		if matchesPrefix(path, route.PathPrefix) {
			return route, true
		}
	}
	return Route{}, false
}

func matchesPrefix(path string, prefix string) bool {
	if prefix == "/" {
		return true
	}
	trimmed := strings.TrimSuffix(prefix, "/")
	if path == trimmed {
		return true
	}
	return strings.HasPrefix(path, trimmed+"/")
}
