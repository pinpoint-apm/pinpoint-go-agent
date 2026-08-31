package ppgorilla

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startAgent(t *testing.T, opts ...pinpoint.ConfigOption) pinpoint.Agent {
	t.Helper()

	opts = append([]pinpoint.ConfigOption{
		pinpoint.WithAppName("testApp"),
		pinpoint.WithAgentId("testAgent"),
	}, opts...)

	config, err := pinpoint.NewConfig(opts...)
	require.NoError(t, err)

	agent, err := pinpoint.NewTestAgent(config, t)
	require.NoError(t, err)
	t.Cleanup(agent.Shutdown)

	return agent
}

// spanOf reads back what the tracer recorded on its span: the RPC name, the
// endpoint, the resolved remote address and whether the span failed.
func spanOf(t *testing.T, tracer pinpoint.Tracer) map[string]interface{} {
	t.Helper()
	require.NotNil(t, tracer, "the handler never ran")
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(tracer.JsonString(), &m))
	return m
}

// The middleware sits in front of every route, so it must leave mux's own
// behaviour intact: route variables still resolve and the handler's status and
// body reach the client unchanged.
func TestMiddleware_PreservesRoutingAndVars(t *testing.T) {
	startAgent(t)

	r := mux.NewRouter()
	r.Use(Middleware())
	r.HandleFunc("/hello/{name}", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hello " + mux.Vars(req)["name"]))
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	assert.Equal(t, http.StatusTeapot, rec.Code)
	assert.Equal(t, "hello pinpoint", rec.Body.String())
}

// The handler reads its tracer out of the request context, so the wrapper has
// to hand the handler the tracer-carrying request.
func TestMiddleware_PutsSampledTracerInRequestContext(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	r := mux.NewRouter()
	r.Use(Middleware())
	r.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(req)
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	require.NotNil(t, tracer, "no tracer in the handler's request context")
	assert.True(t, tracer.IsSampled(), "handler received an unsampled tracer")
	assert.NotEmpty(t, tracer.TransactionId().String())
}

// The span is what shows up in Pinpoint, so the request attributes it carries
// have to come from the request rather than defaults.
func TestMiddleware_RecordsRequestAttributesOnTheSpan(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	r := mux.NewRouter()
	r.Use(Middleware())
	r.HandleFunc("/hello/{name}", func(w http.ResponseWriter, req *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(req)
	})

	req := httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil)
	req.Host = "myhost:8080"
	req.RemoteAddr = "10.0.0.1:4242"
	req.Header.Set("X-Real-Ip", "203.0.113.9")
	r.ServeHTTP(httptest.NewRecorder(), req)

	span := spanOf(t, tracer)
	assert.Equal(t, "/hello/pinpoint", span["RpcName"], "the span is named after the request path, not the route template")
	assert.Equal(t, "myhost:8080", span["EndPoint"])
	assert.Equal(t, "203.0.113.9", span["RemoteAddr"], "X-Real-Ip must win over the transport peer address")
}

// A mux service is usually one hop of a larger call: the tracing headers the
// caller sent have to put this span in the caller's transaction.
func TestMiddleware_ContinuesTheCallersTransaction(t *testing.T) {
	startAgent(t)

	caller := pinpoint.GetAgent().NewSpanTracer("caller", "/caller")
	defer caller.EndSpan()
	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	caller.NewSpanEvent("call")
	caller.Inject(req.Header)
	caller.EndSpanEvent()

	var tracer pinpoint.Tracer
	r := mux.NewRouter()
	r.Use(Middleware())
	r.HandleFunc("/hello", func(w http.ResponseWriter, req *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(req)
	})

	r.ServeHTTP(httptest.NewRecorder(), req)

	require.NotNil(t, tracer)
	assert.Equal(t, caller.TransactionId().String(), tracer.TransactionId().String())
}

// The status the span records comes from the response writer the wrapper
// substituted, so it has to follow whatever the handler wrote.
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
			handler:    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
			wantStatus: http.StatusServiceUnavailable,
			wantFail:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			startAgent(t)

			var tracer pinpoint.Tracer
			r := mux.NewRouter()
			r.Use(Middleware())
			r.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
				tracer = pinpoint.TracerFromRequestContext(req)
				tt.handler(w, req)
			})

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantFail, spanOf(t, tracer)["Err"] != float64(0),
				"the default 5xx error class decides whether the span fails")
		})
	}
}

// A request no route matches is answered by mux's own 404 handler, which the
// middleware does not wrap; the router must still answer it.
func TestMiddleware_UnmatchedRoute(t *testing.T) {
	startAgent(t)

	r := mux.NewRouter()
	r.Use(Middleware())
	r.HandleFunc("/hello", func(w http.ResponseWriter, req *http.Request) {})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nowhere", nil))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// Subrouters build their own middleware chain; a middleware registered on the
// parent has to reach the routes a subrouter owns.
func TestMiddleware_OnASubrouter(t *testing.T) {
	startAgent(t, pinpoint.WithHttpUrlStatEnable(true))

	var tracer pinpoint.Tracer
	var pathTemplate string
	r := mux.NewRouter()
	r.Use(Middleware())
	api := r.PathPrefix("/api").Subrouter()
	api.HandleFunc("/hello/{name}", func(w http.ResponseWriter, req *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(req)
		pathTemplate, _ = mux.CurrentRoute(req).GetPathTemplate()
		w.WriteHeader(http.StatusTeapot)
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/hello/pinpoint", nil))

	assert.Equal(t, http.StatusTeapot, rec.Code)
	assert.Equal(t, "/api/hello/{name}", pathTemplate)
	require.NotNil(t, tracer)
	assert.True(t, tracer.IsSampled())
	assert.Equal(t, "/api/hello/pinpoint", spanOf(t, tracer)["RpcName"])
}

// mux.CurrentRoute returns nil whenever a wrapped handler runs outside a mux
// router, so the URL-stat path lookup - which runs in a defer, after the
// handler - must tolerate its absence instead of dereferencing nil.
func TestWrapHandlerFunc_OutsideAMuxRouter(t *testing.T) {
	startAgent(t, pinpoint.WithHttpUrlStatEnable(true))

	var tracer pinpoint.Tracer
	h := WrapHandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(req)
		require.Nil(t, mux.CurrentRoute(req), "this handler is deliberately mounted outside a mux router")
		w.WriteHeader(http.StatusNoContent)
	})

	srv := http.NewServeMux()
	srv.HandleFunc("/plain", h)

	rec := httptest.NewRecorder()
	assert.NotPanics(t, func() {
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plain", nil))
	}, "the deferred path lookup dereferenced a nil route")

	require.NotNil(t, tracer, "the handler did not run")
	assert.True(t, tracer.IsSampled())
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

// WrapHandler instruments one route instead of the whole router; inside a mux
// router the route template is available to the deferred URL-stat collection.
func TestWrapHandler_InsideAMuxRouter(t *testing.T) {
	startAgent(t, pinpoint.WithHttpUrlStatEnable(true))

	var pathTemplate string
	var tracer pinpoint.Tracer
	r := mux.NewRouter()
	r.Handle("/hello/{name}", WrapHandler(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(req)
		pathTemplate, _ = mux.CurrentRoute(req).GetPathTemplate()
		assert.Equal(t, "pinpoint", mux.Vars(req)["name"], "the route variable must survive the wrapper")
	})))

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	assert.Equal(t, "/hello/{name}", pathTemplate)
	require.NotNil(t, tracer)
	assert.True(t, tracer.IsSampled())
}

// The wrapper marks the span failed and re-panics; swallowing the panic would
// turn a crash the server reports into a silent 200.
func TestMiddleware_PanicPropagates(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	r := mux.NewRouter()
	r.Use(Middleware())
	r.HandleFunc("/boom", func(w http.ResponseWriter, req *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(req)
		panic("boom")
	})

	assert.PanicsWithValue(t, "boom", func() {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
	}, "the wrapper swallowed the handler panic")

	assert.NotEqual(t, float64(0), spanOf(t, tracer)["Err"], "a panicking handler must fail the span")
}

// With no agent running the middleware must be a straight pass-through.
func TestMiddleware_PassesThroughWhenAgentDisabled(t *testing.T) {
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

	called := false
	r := mux.NewRouter()
	r.Use(Middleware())
	r.HandleFunc("/hello/{name}", func(w http.ResponseWriter, req *http.Request) {
		called = true
		assert.Equal(t, "pinpoint", mux.Vars(req)["name"])
		assert.False(t, pinpoint.TracerFromRequestContext(req).IsSampled(),
			"a disabled agent produced a sampled tracer")
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	require.True(t, called, "the handler did not run")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// WrapHandlerFunc is the other entry point and has to pass through too.
func TestWrapHandlerFunc_PassesThroughWhenAgentDisabled(t *testing.T) {
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

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
