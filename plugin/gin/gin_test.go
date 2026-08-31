package ppgin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

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

// serve runs one request through r and returns the recorder.
func serve(r *gin.Engine, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// The middleware sits in front of every route, so it must leave gin's own
// behaviour intact: the matched route still runs with its path parameters, and
// the handler's status and body reach the client unchanged.
func TestMiddleware_PreservesRouting(t *testing.T) {
	startAgent(t)

	r := gin.New()
	r.Use(Middleware())
	r.GET("/hello/:name", func(c *gin.Context) {
		c.String(http.StatusTeapot, "hello %s (%s)", c.Param("name"), c.FullPath())
	})

	rec := serve(r, httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	assert.Equal(t, http.StatusTeapot, rec.Code)
	assert.Equal(t, "hello pinpoint (/hello/:name)", rec.Body.String())
}

// The handler reads its tracer out of the request context, so the middleware
// has to replace c.Request with the tracer-carrying one before calling Next.
func TestMiddleware_PutsSampledTracerInRequestContext(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	r := gin.New()
	r.Use(Middleware())
	r.GET("/", func(c *gin.Context) {
		tracer = pinpoint.FromContext(c.Request.Context())
	})

	serve(r, httptest.NewRequest(http.MethodGet, "/", nil))

	require.NotNil(t, tracer, "no tracer in the handler's request context")
	assert.True(t, tracer.IsSampled(), "handler received an unsampled tracer")
	assert.NotEmpty(t, tracer.TransactionId().String(), "handler received a tracer without a transaction id")
}

// The span is what shows up in Pinpoint, so the request attributes it carries
// have to come from the gin request rather than defaults.
func TestMiddleware_RecordsRequestAttributesOnTheSpan(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	r := gin.New()
	r.Use(Middleware())
	r.GET("/hello/:name", func(c *gin.Context) {
		tracer = pinpoint.FromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil)
	req.Host = "myhost:8080"
	req.RemoteAddr = "10.0.0.1:4242"
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.2")
	serve(r, req)

	span := spanOf(t, tracer)
	assert.Equal(t, "/hello/pinpoint", span["RpcName"], "the span is named after the request path, not the route pattern")
	assert.Equal(t, "myhost:8080", span["EndPoint"])
	assert.Equal(t, "203.0.113.7", span["RemoteAddr"], "X-Forwarded-For must win over the transport peer address")
}

// A gin service is usually one hop of a larger call: the tracing headers the
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
	r := gin.New()
	r.Use(Middleware())
	r.GET("/hello", func(c *gin.Context) { tracer = pinpoint.FromContext(c.Request.Context()) })

	serve(r, req)

	require.NotNil(t, tracer)
	assert.Equal(t, caller.TransactionId().String(), tracer.TransactionId().String())
}

// The status the span records is gin's, taken after the chain has run: a
// handler that sets it late or through Abort must still be reported correctly.
func TestMiddleware_RecordsTheFinalStatus(t *testing.T) {
	tests := []struct {
		name       string
		handler    gin.HandlerFunc
		wantStatus int
		wantFail   bool
	}{
		{
			name:       "an explicit status",
			handler:    func(c *gin.Context) { c.String(http.StatusTeapot, "teapot") },
			wantStatus: http.StatusTeapot,
		},
		{
			name:       "no status at all leaves gin's implicit 200",
			handler:    func(c *gin.Context) {},
			wantStatus: http.StatusOK,
		},
		{
			name:       "AbortWithStatus",
			handler:    func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) },
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "a configured error status fails the span",
			handler:    func(c *gin.Context) { c.String(http.StatusInternalServerError, "boom") },
			wantStatus: http.StatusInternalServerError,
			wantFail:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			startAgent(t)

			var tracer pinpoint.Tracer
			r := gin.New()
			r.Use(Middleware())
			r.GET("/", func(c *gin.Context) {
				tracer = pinpoint.FromContext(c.Request.Context())
				tt.handler(c)
			})

			rec := serve(r, httptest.NewRequest(http.MethodGet, "/", nil))

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantFail, spanOf(t, tracer)["Err"] != float64(0),
				"the default 5xx error class decides whether the span fails")
		})
	}
}

// A middleware registered after this one may abort the chain; the wrapper has
// to end its span all the same and report what the client actually received.
func TestMiddleware_AbortedChain(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	handlerRan := false
	r := gin.New()
	r.Use(Middleware())
	r.Use(func(c *gin.Context) {
		tracer = pinpoint.FromContext(c.Request.Context())
		c.AbortWithStatus(http.StatusForbidden)
	})
	r.GET("/", func(c *gin.Context) { handlerRan = true })

	rec := serve(r, httptest.NewRequest(http.MethodGet, "/", nil))

	assert.False(t, handlerRan, "the aborting middleware should have stopped the chain")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.True(t, spanOf(t, tracer) != nil)
}

// An unmatched route still runs the global middleware, and c.FullPath() is
// empty there; collecting the URL statistic must not choke on that.
func TestMiddleware_UnmatchedRoute(t *testing.T) {
	startAgent(t, pinpoint.WithHttpUrlStatEnable(true))

	var tracer pinpoint.Tracer
	r := gin.New()
	r.Use(Middleware())
	r.NoRoute(func(c *gin.Context) {
		tracer = pinpoint.FromContext(c.Request.Context())
		c.String(http.StatusNotFound, "not found")
	})

	rec := serve(r, httptest.NewRequest(http.MethodGet, "/nowhere", nil))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "/nowhere", spanOf(t, tracer)["RpcName"])
}

// WrapHandler instruments one route instead of the whole router, and has to
// give that handler the same tracer-carrying request the middleware does.
func TestWrapHandler_PutsSampledTracerInRequestContext(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	r := gin.New()
	r.GET("/wrapped", WrapHandler(func(c *gin.Context) {
		tracer = pinpoint.FromContext(c.Request.Context())
		c.Status(http.StatusNoContent)
	}))

	rec := serve(r, httptest.NewRequest(http.MethodGet, "/wrapped", nil))

	require.NotNil(t, tracer)
	assert.True(t, tracer.IsSampled(), "wrapped handler received an unsampled tracer")
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "/wrapped", spanOf(t, tracer)["RpcName"])
}

// WrapHandler names its span event after the wrapped function, so a handler
// that is not a plain func must not break the name lookup.
func TestWrapHandler_HandlerName(t *testing.T) {
	startAgent(t)

	assert.Contains(t, pphttp.HandlerFuncName(gin.HandlerFunc(func(c *gin.Context) {})), "()")
	assert.NotPanics(t, func() {
		r := gin.New()
		r.GET("/wrapped", WrapHandler(func(c *gin.Context) { c.Status(http.StatusOK) }))
		serve(r, httptest.NewRequest(http.MethodGet, "/wrapped", nil))
	})
}

// The wrapper marks the span failed and re-panics; swallowing the panic would
// turn a crash gin's Recovery middleware reports into a silent 200.
func TestMiddleware_RepanicsAndLetsRecoveryRespond(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	r := gin.New()
	r.Use(gin.Recovery(), Middleware())
	r.GET("/boom", func(c *gin.Context) {
		tracer = pinpoint.FromContext(c.Request.Context())
		panic("boom")
	})

	rec := serve(r, httptest.NewRequest(http.MethodGet, "/boom", nil))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotEqual(t, float64(0), spanOf(t, tracer)["Err"], "a panicking handler must fail the span")
}

// Without a recovery middleware the panic must still reach the caller.
func TestMiddleware_PanicPropagates(t *testing.T) {
	startAgent(t)

	r := gin.New()
	r.Use(Middleware())
	r.GET("/boom", func(c *gin.Context) { panic("boom") })

	assert.PanicsWithValue(t, "boom", func() {
		serve(r, httptest.NewRequest(http.MethodGet, "/boom", nil))
	}, "the wrapper swallowed the handler panic")
}

// c.Error is gin's way of reporting a failure without aborting; the wrapper
// records the last one, and must not disturb the response gin sends.
func TestMiddleware_HandlerErrorsDoNotChangeTheResponse(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	r := gin.New()
	r.Use(Middleware())
	r.GET("/err", func(c *gin.Context) {
		tracer = pinpoint.FromContext(c.Request.Context())
		_ = c.Error(errors.New("first"))
		_ = c.Error(errors.New("last"))
		c.String(http.StatusBadRequest, "bad")
	})

	rec := serve(r, httptest.NewRequest(http.MethodGet, "/err", nil))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "bad", rec.Body.String())
	assert.NotEqual(t, float64(0), spanOf(t, tracer)["Err"], "a handler error must fail the span")
}

// With no agent running the middleware must be a straight pass-through: no
// tracer in the context and no panic from tracing a disabled agent.
func TestMiddleware_PassesThroughWhenAgentDisabled(t *testing.T) {
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

	called := false
	r := gin.New()
	r.Use(Middleware())
	r.GET("/", func(c *gin.Context) {
		called = true
		assert.False(t, pinpoint.FromContext(c.Request.Context()).IsSampled(),
			"a disabled agent produced a sampled tracer")
		c.Status(http.StatusOK)
	})

	rec := serve(r, httptest.NewRequest(http.MethodGet, "/", nil))

	require.True(t, called, "the handler did not run")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// WrapHandler is the other entry point, and it has to pass through too.
func TestWrapHandler_PassesThroughWhenAgentDisabled(t *testing.T) {
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

	called := false
	r := gin.New()
	r.GET("/wrapped", WrapHandler(func(c *gin.Context) {
		called = true
		c.Status(http.StatusNoContent)
	}))

	rec := serve(r, httptest.NewRequest(http.MethodGet, "/wrapped", nil))

	require.True(t, called, "the handler did not run")
	assert.Equal(t, http.StatusNoContent, rec.Code)
}
