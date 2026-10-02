package pphttprouter

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/julienschmidt/httprouter"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every method helper, and Handle, has to register the wrapped handler under
// the same path and method, or the route silently disappears. Each one is
// registered on its own router and driven end to end.
func TestRouter_AllMethodsStayRouted(t *testing.T) {
	pptest.StartAgent(t)

	for _, tt := range []struct {
		method   string
		register func(*Router, string, httprouter.Handle)
	}{
		{http.MethodGet, (*Router).GET},
		{http.MethodHead, (*Router).HEAD},
		{http.MethodOptions, (*Router).OPTIONS},
		{http.MethodPost, (*Router).POST},
		{http.MethodPut, (*Router).PUT},
		{http.MethodPatch, (*Router).PATCH},
		{http.MethodDelete, (*Router).DELETE},
		// Handle takes the method as an argument rather than from the helper name.
		{http.MethodTrace, func(r *Router, p string, h httprouter.Handle) { r.Handle(http.MethodTrace, p, h) }},
	} {
		t.Run(tt.method, func(t *testing.T) {
			r := New()
			var name string
			var tracer pinpoint.Tracer
			tt.register(r, "/hello/:name", func(w http.ResponseWriter, req *http.Request, p httprouter.Params) {
				name = p.ByName("name")
				tracer = pinpoint.TracerFromRequestContext(req)
				w.WriteHeader(http.StatusNoContent)
			})

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(tt.method, "/hello/pinpoint", nil))

			assert.Equal(t, "pinpoint", name, "route parameter")
			assert.Equal(t, http.StatusNoContent, rec.Code)
			require.NotNil(t, tracer, "the wrapper for %s did not trace", tt.method)
			assert.True(t, tracer.IsSampled(), "%s handler received an unsampled tracer", tt.method)
		})
	}
}

// A method the router has no route for is answered by httprouter itself and
// never reaches an instrumented handler.
func TestRouter_MethodNotAllowed(t *testing.T) {
	pptest.StartAgent(t)

	r := New()
	r.GET("/hello", func(http.ResponseWriter, *http.Request, httprouter.Params) {
		t.Error("the GET handler ran for a POST request")
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/hello", nil))

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

// Handler and HandlerFunc adapt a net/http handler, which reads its route
// parameters from the request context rather than from an argument. The
// wrapper replaces the request to add the tracer, so the params it stored have
// to survive that replacement.
func TestRouter_HandlerKeepsParamsInRequestContext(t *testing.T) {
	pptest.StartAgent(t)

	for _, tt := range []struct {
		name     string
		register func(*Router, string, http.HandlerFunc)
	}{
		{"Handler", func(r *Router, p string, h http.HandlerFunc) { r.Handler(http.MethodGet, p, h) }},
		{"HandlerFunc", func(r *Router, p string, h http.HandlerFunc) { r.HandlerFunc(http.MethodGet, p, h) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := New()
			var name string
			var tracer pinpoint.Tracer
			tt.register(r, "/hello/:name", func(w http.ResponseWriter, req *http.Request) {
				name = httprouter.ParamsFromContext(req.Context()).ByName("name")
				tracer = pinpoint.TracerFromRequestContext(req)
			})

			r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

			assert.Equal(t, "pinpoint", name, "route parameter")
			require.NotNil(t, tracer)
			assert.True(t, tracer.IsSampled(), "handler received an unsampled tracer")
		})
	}
}

// A route with no parameters skips the context copy entirely; the tracer still
// has to reach the handler.
func TestRouter_HandlerWithoutParams(t *testing.T) {
	pptest.StartAgent(t)

	r := New()
	var tracer pinpoint.Tracer
	r.HandlerFunc(http.MethodGet, "/plain", func(w http.ResponseWriter, req *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(req)
		assert.Empty(t, httprouter.ParamsFromContext(req.Context()))
		w.WriteHeader(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plain", nil))

	require.NotNil(t, tracer)
	assert.True(t, tracer.IsSampled())
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

// The span is what shows up in Pinpoint, so the request attributes it carries
// have to come from the request rather than defaults.
func TestRouter_RecordsRequestAttributesOnTheSpan(t *testing.T) {
	pptest.StartAgent(t)

	r := New()
	var tracer pinpoint.Tracer
	r.GET("/hello/:name", func(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
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

// An httprouter service is usually one hop of a larger call: the tracing
// headers the caller sent have to put this span in the caller's transaction.
func TestRouter_ContinuesTheCallersTransaction(t *testing.T) {
	pptest.StartAgent(t)

	caller := pinpoint.GetAgent().NewSpanTracer("caller", "/caller")
	defer caller.EndSpan()
	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	caller.NewSpanEvent("call")
	caller.Inject(req.Header)
	caller.EndSpanEvent()

	r := New()
	var tracer pinpoint.Tracer
	r.GET("/hello", func(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
		tracer = pinpoint.TracerFromRequestContext(req)
	})

	r.ServeHTTP(httptest.NewRecorder(), req)

	require.NotNil(t, tracer)
	assert.Equal(t, caller.TransactionId().String(), tracer.TransactionId().String())
}

// The status the span records comes from the response writer the wrapper
// substituted, so it has to follow whatever the handler wrote.
func TestRouter_RecordsTheFinalStatus(t *testing.T) {
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
			handler:    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
			wantStatus: http.StatusInternalServerError,
			wantFail:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pptest.StartAgent(t)

			r := New()
			var tracer pinpoint.Tracer
			r.GET("/", func(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
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

// The wrapper marks the span failed and re-panics; swallowing the panic would
// turn a crash httprouter's PanicHandler reports into a silent 200.
func TestRouter_RepanicsIntoThePanicHandler(t *testing.T) {
	pptest.StartAgent(t)

	r := New()
	recovered := false
	r.PanicHandler = func(w http.ResponseWriter, req *http.Request, _ interface{}) {
		recovered = true
		w.WriteHeader(http.StatusInternalServerError)
	}
	var tracer pinpoint.Tracer
	r.GET("/boom", func(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
		tracer = pinpoint.TracerFromRequestContext(req)
		panic("boom")
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	assert.True(t, recovered, "the wrapper swallowed the handler panic")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotEqual(t, float64(0), pptest.SpanOf(t, tracer)["Err"], "a panicking handler must fail the span")
}

// Without a PanicHandler the panic must still reach the caller.
func TestRouter_PanicPropagates(t *testing.T) {
	pptest.StartAgent(t)

	r := New()
	r.GET("/boom", func(http.ResponseWriter, *http.Request, httprouter.Params) { panic("boom") })

	assert.PanicsWithValue(t, "boom", func() {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
	})
}

// With no agent running the wrapper must be a straight pass-through.
func TestRouter_PassesThroughWhenAgentDisabled(t *testing.T) {
	r := New()
	called := false
	r.GET("/hello/:name", func(w http.ResponseWriter, req *http.Request, p httprouter.Params) {
		called = true
		assert.Equal(t, "pinpoint", p.ByName("name"), "route parameter")
		assert.False(t, pinpoint.TracerFromRequestContext(req).IsSampled(),
			"a disabled agent produced a sampled tracer")
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	require.True(t, called, "the handler did not run")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// WrapHandle is the other entry point and has to pass through too.
func TestWrapHandle_PassesThroughWhenAgentDisabled(t *testing.T) {
	r := httprouter.New()
	called := false
	r.GET("/hello/:name", WrapHandle(func(w http.ResponseWriter, req *http.Request, p httprouter.Params) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	require.True(t, called, "the handler did not run")
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

// WrapHandle given the route pattern collects the URL statistic under it and
// still traces and routes the call; the one-argument form keeps working.
func TestWrapHandle_WithPathCollectsUrlStat(t *testing.T) {
	pptest.StartAgent(t, pinpoint.WithHttpUrlStatEnable(true))

	r := httprouter.New()
	var tracer pinpoint.Tracer
	r.GET("/hello/:name", WrapHandle(func(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
		tracer = pinpoint.TracerFromRequestContext(req)
		w.WriteHeader(http.StatusNoContent)
	}, "/hello/:name"))
	r.GET("/plain", WrapHandle(func(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	assert.NotPanics(t, func() {
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))
	})
	assert.Equal(t, http.StatusNoContent, rec.Code)
	require.NotNil(t, tracer)
	assert.True(t, tracer.IsSampled())
	assert.Equal(t, "/hello/pinpoint", pptest.SpanOf(t, tracer)["RpcName"])

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plain", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}
