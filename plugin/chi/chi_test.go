package ppchi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/pinpoint-apm/pinpoint-go-agent"
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

func startAgent(t *testing.T) {
	t.Helper()
	config, err := pinpoint.NewConfig(pinpoint.WithAppName("testApp"), pinpoint.WithAgentId("testAgent"))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := pinpoint.NewTestAgent(config, t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Shutdown)
}

// The middleware sits in front of every route, so it must leave chi's own
// behaviour intact: URL parameters still resolve and the handler's status and
// body reach the client unchanged.
func TestMiddleware_PreservesRouting(t *testing.T) {
	startAgent(t)

	r := chi.NewRouter()
	r.Use(Middleware())
	r.Get("/hello/{name}", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hello " + chi.URLParam(req, "name")))
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
	if want := "hello pinpoint"; rec.Body.String() != want {
		t.Errorf("body = %q, want %q", rec.Body.String(), want)
	}
}

// The handler reads its tracer out of the request context, so the wrapper has
// to hand the handler the tracer-carrying request.
func TestMiddleware_PutsSampledTracerInRequestContext(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	r := chi.NewRouter()
	r.Use(Middleware())
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(req)
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if tracer == nil {
		t.Fatal("no tracer in the handler's request context")
	}
	if !tracer.IsSampled() {
		t.Error("handler received an unsampled tracer")
	}
}

// Inside a chi router the route pattern is available to the deferred URL-stat
// collection, mounted routes included.
func TestMiddleware_RoutePatternInsideARouter(t *testing.T) {
	startAgent(t)

	var pattern string
	r := chi.NewRouter()
	r.Use(Middleware())
	r.Route("/api", func(r chi.Router) {
		r.Get("/hello/{name}", func(w http.ResponseWriter, req *http.Request) {
			pattern = routePattern(req)
		})
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/hello/pinpoint", nil))

	if want := "/api/hello/{name}"; pattern != want {
		t.Errorf("route pattern = %q, want %q", pattern, want)
	}
}

// WrapHandlerFunc can be mounted outside a chi router, where chi.RouteContext
// returns nil. The wrapper still has to trace the call and serve the request.
func TestWrapHandlerFunc_OutsideAChiRouter(t *testing.T) {
	startAgent(t)

	var sampled bool
	h := WrapHandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		sampled = pinpoint.TracerFromRequestContext(req).IsSampled()
		w.WriteHeader(http.StatusNoContent)
	})

	srv := http.NewServeMux()
	srv.HandleFunc("/plain", h)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plain", nil))

	if !sampled {
		t.Error("handler received an unsampled tracer")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

// WrapHandler instruments one route instead of the whole router.
func TestWrapHandler_InsideAChiRouter(t *testing.T) {
	startAgent(t)

	var sampled bool
	r := chi.NewRouter()
	r.Method(http.MethodGet, "/hello/{name}", WrapHandler(http.HandlerFunc(
		func(w http.ResponseWriter, req *http.Request) {
			sampled = pinpoint.TracerFromRequestContext(req).IsSampled()
		})))

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	if !sampled {
		t.Error("wrapped handler received an unsampled tracer")
	}
}

// The wrapper marks the span failed and re-panics; swallowing the panic would
// turn a crash chi's Recoverer reports into a silent 200.
func TestMiddleware_PanicPropagates(t *testing.T) {
	startAgent(t)

	r := chi.NewRouter()
	r.Use(Middleware())
	r.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })

	defer func() {
		if recover() == nil {
			t.Error("the wrapper swallowed the handler panic")
		}
	}()
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
}

// With no agent running the middleware must be a straight pass-through.
func TestMiddleware_PassesThroughWhenAgentDisabled(t *testing.T) {
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

	called := false
	r := chi.NewRouter()
	r.Use(Middleware())
	r.Get("/hello/{name}", func(w http.ResponseWriter, req *http.Request) {
		called = true
		if chi.URLParam(req, "name") != "pinpoint" {
			t.Errorf("URL parameter = %q, want %q", chi.URLParam(req, "name"), "pinpoint")
		}
		if pinpoint.TracerFromRequestContext(req).IsSampled() {
			t.Error("a disabled agent produced a sampled tracer")
		}
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	if !called {
		t.Fatal("the handler did not run")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}
