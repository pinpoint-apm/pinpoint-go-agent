package ppchi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// chi.RouteContext returns nil outside a chi router, so the wrapper's pattern
// lookup must tolerate its absence instead of dereferencing nil.
func Test_routePattern(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/hello", nil)
	if got := routePattern(r); got != "" {
		t.Errorf("routePattern() without a chi route context = %q, want empty", got)
	}

	rctx := chi.NewRouteContext()
	rctx.RoutePatterns = []string{"/hello/{name}"}
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	if got := routePattern(r); got != "/hello/{name}" {
		t.Errorf("routePattern() = %q, want /hello/{name}", got)
	}
}
