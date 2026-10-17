package gateway

import (
	"sort"
	"strings"

	"github.com/parthparmar-2006/headgate/internal/limiter"
)

const (
	ScopeKey    = "key"
	ScopeTenant = "tenant"
	ScopeRoute  = "route"
)

type Route struct {
	Name       string
	PathPrefix string
	Limits     []RouteLimit
	Limiter    limiter.Limiter
	FailOpen   bool
}

type RouteLimit struct {
	Scope string
	Rule  limiter.Rule
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
