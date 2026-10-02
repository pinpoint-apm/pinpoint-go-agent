package ppchi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chi.RouteContext returns nil outside a chi router, so the wrapper's pattern
// lookup must tolerate its absence instead of dereferencing nil.
func Test_routePattern(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/hello", nil)
	assert.Equal(t, "", routePattern(r), "there is no route pattern outside a chi router")

	rctx := chi.NewRouteContext()
	rctx.RoutePatterns = []string{"/hello/{name}"}
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	assert.Equal(t, "/hello/{name}", routePattern(r))

	// An empty route context is what chi hands a handler it never matched.
	empty := httptest.NewRequest(http.MethodGet, "/hello", nil)
	empty = empty.WithContext(context.WithValue(empty.Context(), chi.RouteCtxKey, chi.NewRouteContext()))
	assert.Equal(t, "", routePattern(empty))
}

// The middleware sits in front of every route, so it must leave chi's own
// behaviour intact: URL parameters still resolve and the handler's status and
// body reach the client unchanged.
func TestMiddleware_PreservesRouting(t *testing.T) {
	pptest.StartAgent(t)

	r := chi.NewRouter()
	r.Use(Middleware())
	r.Get("/hello/{name}", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hello " + chi.URLParam(req, "name")))
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	assert.Equal(t, http.StatusTeapot, rec.Code)
	assert.Equal(t, "hello pinpoint", rec.Body.String())
}

// The span is what shows up in Pinpoint, so the request attributes it carries
// have to come from the request rather than defaults.
func TestMiddleware_RecordsRequestAttributesOnTheSpan(t *testing.T) {
	pptest.StartAgent(t)

	var tracer pinpoint.Tracer
	r := chi.NewRouter()
	r.Use(Middleware())
	r.Get("/hello/{name}", func(w http.ResponseWriter, req *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(req)
	})

	req := httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil)
	req.Host = "myhost:8080"
	req.RemoteAddr = "10.0.0.1:4242"
	r.ServeHTTP(httptest.NewRecorder(), req)

	span := pptest.SpanOf(t, tracer)
	assert.Equal(t, "/hello/pinpoint", span["RpcName"], "the span is named after the request path, not the route pattern")
	assert.Equal(t, "myhost:8080", span["EndPoint"])
	assert.Equal(t, "10.0.0.1", span["RemoteAddr"])
}

// A chi service is usually one hop of a larger call: the tracing headers the
// caller sent have to put this span in the caller's transaction.
func TestMiddleware_ContinuesTheCallersTransaction(t *testing.T) {
	pptest.StartAgent(t)

	caller := pinpoint.GetAgent().NewSpanTracer("caller", "/caller")
	defer caller.EndSpan()
	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	caller.NewSpanEvent("call")
	caller.Inject(req.Header)
	caller.EndSpanEvent()

	var tracer pinpoint.Tracer
	r := chi.NewRouter()
	r.Use(Middleware())
	r.Get("/hello", func(w http.ResponseWriter, req *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(req)
	})

	r.ServeHTTP(httptest.NewRecorder(), req)

	require.NotNil(t, tracer)
	assert.Equal(t, caller.TransactionId().String(), tracer.TransactionId().String())
}

// The status the span records comes from the response writer the wrapper
// substituted, so it has to follow whatever the handler wrote - including the
// implicit 200 of a handler that never calls WriteHeader.
func TestMiddleware_RecordsTheFinalStatus(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantStatus int
		wantFail   bool
	}{
		{
			name:       "an explicit status",
			handler:    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) },
			wantStatus: http.StatusTeapot,
		},
		{
			name:       "a body without a status leaves the implicit 200",
			handler:    func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hi")) },
			wantStatus: http.StatusOK,
		},
		{
			name:       "a configured error status fails the span",
			handler:    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
			wantStatus: http.StatusBadGateway,
			wantFail:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pptest.StartAgent(t)

			var tracer pinpoint.Tracer
			r := chi.NewRouter()
			r.Use(Middleware())
			r.Get("/", func(w http.ResponseWriter, req *http.Request) {
				tracer = pinpoint.TracerFromRequestContext(req)
				tt.handler(w, req)
			})

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantFail, pptest.SpanOf(t, tracer)["Err"] != float64(0),
				"the default 5xx error class decides whether the span fails")
		})
	}
}

// Inside a chi router the route pattern is available to the deferred URL-stat
// collection, mounted routes included.
func TestMiddleware_RoutePatternInsideARouter(t *testing.T) {
	pptest.StartAgent(t, pinpoint.WithHttpUrlStatEnable(true))

	var pattern string
	r := chi.NewRouter()
	r.Use(Middleware())
	r.Route("/api", func(r chi.Router) {
		r.Get("/hello/{name}", func(w http.ResponseWriter, req *http.Request) {
			pattern = routePattern(req)
		})
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/hello/pinpoint", nil))

	assert.Equal(t, "/api/hello/{name}", pattern)
}

// chi middlewares wrap the response writer too; the wrapper has to keep the
// chain's own writer working rather than shadowing it.
func TestMiddleware_ComposesWithOtherChiMiddleware(t *testing.T) {
	pptest.StartAgent(t)

	var tracer pinpoint.Tracer
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(Middleware())
	r.Use(middleware.StripSlashes)
	r.Get("/hello", func(w http.ResponseWriter, req *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(req)
		assert.NotEmpty(t, middleware.GetReqID(req.Context()), "an outer middleware's context value was lost")
		w.WriteHeader(http.StatusTeapot)
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello/", nil))

	assert.Equal(t, http.StatusTeapot, rec.Code)
	require.NotNil(t, tracer)
	assert.True(t, tracer.IsSampled())
}

// WrapHandler instruments one route instead of the whole router.
func TestWrapHandler_InsideAChiRouter(t *testing.T) {
	pptest.StartAgent(t)

	var tracer pinpoint.Tracer
	r := chi.NewRouter()
	r.Method(http.MethodGet, "/hello/{name}", WrapHandler(http.HandlerFunc(
		func(w http.ResponseWriter, req *http.Request) {
			tracer = pinpoint.TracerFromRequestContext(req)
			assert.Equal(t, "pinpoint", chi.URLParam(req, "name"), "the URL parameter must survive the wrapper")
			w.WriteHeader(http.StatusTeapot)
		})))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	require.NotNil(t, tracer)
	assert.True(t, tracer.IsSampled(), "wrapped handler received an unsampled tracer")
	assert.Equal(t, http.StatusTeapot, rec.Code)
}

// The wrapper marks the span failed and re-panics; swallowing the panic would
// turn a crash chi's Recoverer reports into a silent 200.
func TestMiddleware_PanicPropagates(t *testing.T) {
	pptest.StartAgent(t)

	var tracer pinpoint.Tracer
	r := chi.NewRouter()
	r.Use(Middleware())
	r.Get("/boom", func(w http.ResponseWriter, req *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(req)
		panic("boom")
	})

	assert.PanicsWithValue(t, "boom", func() {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
	}, "the wrapper swallowed the handler panic")

	assert.NotEqual(t, float64(0), pptest.SpanOf(t, tracer)["Err"], "a panicking handler must fail the span")
}

// With chi's Recoverer in front, the panic becomes a 500 and the span still
// has to be closed and marked failed.
func TestMiddleware_RepanicsAndLetsRecovererRespond(t *testing.T) {
	pptest.StartAgent(t)

	var tracer pinpoint.Tracer
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(Middleware())
	r.Get("/boom", func(w http.ResponseWriter, req *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(req)
		panic("boom")
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotEqual(t, float64(0), pptest.SpanOf(t, tracer)["Err"])
}

// With no agent running the middleware must be a straight pass-through.
func TestMiddleware_PassesThroughWhenAgentDisabled(t *testing.T) {
	called := false
	r := chi.NewRouter()
	r.Use(Middleware())
	r.Get("/hello/{name}", func(w http.ResponseWriter, req *http.Request) {
		called = true
		assert.Equal(t, "pinpoint", chi.URLParam(req, "name"))
		assert.False(t, pinpoint.TracerFromRequestContext(req).IsSampled(),
			"a disabled agent produced a sampled tracer")
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	require.True(t, called, "the handler did not run")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// WrapHandler is the other entry point and has to pass through too.
func TestWrapHandler_PassesThroughWhenAgentDisabled(t *testing.T) {
	called := false
	h := WrapHandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/plain", nil))

	require.True(t, called, "the handler did not run")
	assert.Equal(t, http.StatusNoContent, rec.Code)
}
