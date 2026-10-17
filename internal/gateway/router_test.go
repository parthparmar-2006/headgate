package gateway

import "testing"

func testRouter() *Router {
	return NewRouter([]Route{
		{Name: "default", PathPrefix: "/"},
		{Name: "payments", PathPrefix: "/payments"},
		{Name: "refunds", PathPrefix: "/payments/refunds"},
		{Name: "admin", PathPrefix: "/admin/"},
	})
}

func TestRouterMatchesLongestPrefix(t *testing.T) {
	cases := map[string]string{
		"/payments":              "payments",
		"/payments/charge":       "payments",
		"/payments/refunds":      "refunds",
		"/payments/refunds/42":   "refunds",
		"/admin/users":           "admin",
		"/admin":                 "admin",
		"/catalog":               "default",
		"/paymentsextra":         "default",
		"/payments/refundsextra": "payments",
	}

	router := testRouter()
	for path, want := range cases {
		route, found := router.Match(path)
		if !found {
			t.Fatalf("%s: no route matched", path)
		}
		if route.Name != want {
			t.Fatalf("%s: matched %q, want %q", path, route.Name, want)
		}
	}
}

func TestRouterWithoutDefaultReportsNoMatch(t *testing.T) {
	router := NewRouter([]Route{{Name: "payments", PathPrefix: "/payments"}})

	_, found := router.Match("/catalog")

	if found {
		t.Fatalf("expected no match")
	}
}
