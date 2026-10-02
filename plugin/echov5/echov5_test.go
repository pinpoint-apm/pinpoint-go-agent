package ppechov5

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An error a handler returns has to be recorded on the span and reach echo's
// HTTPErrorHandler exactly once - by echo, from the returned error - not once
// by the wrapper and again by echo, in both instrumentation forms.
func TestHandlerError_RecordedAndHandledOnce(t *testing.T) {
	for _, tt := range []struct {
		name  string
		route func(e *echo.Echo, h echo.HandlerFunc)
	}{
		{"WrapHandler", func(e *echo.Echo, h echo.HandlerFunc) { e.GET("/boom", WrapHandler(h)) }},
		{"Middleware", func(e *echo.Echo, h echo.HandlerFunc) { e.Use(Middleware()); e.GET("/boom", h) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pptest.StartAgent(t)

			e := echo.New()
			calls := 0
			e.HTTPErrorHandler = func(c *echo.Context, err error) {
				calls++
				echo.DefaultHTTPErrorHandler(false)(c, err)
			}
			var tracer pinpoint.Tracer
			tt.route(e, func(c *echo.Context) error {
				tracer = pinpoint.TracerFromRequestContext(c.Request())
				return echo.NewHTTPError(http.StatusTeapot, "boom")
			})

			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

			assert.Equal(t, 1, calls, "HTTPErrorHandler ran more than once for one failed request")
			assert.Equal(t, http.StatusTeapot, rec.Code)
			assert.NotEqual(t, float64(0), pptest.SpanOf(t, tracer)["Err"], "the handler error must be recorded on the span")
		})
	}
}

// The middleware sits in front of every route, so it must leave echo's own
// behaviour intact: route parameters still resolve and the handler's status and
// body reach the client unchanged.
func TestMiddleware_PreservesRouting(t *testing.T) {
	pptest.StartAgent(t)

	e := echo.New()
	e.Use(Middleware())
	e.GET("/hello/:name", func(c *echo.Context) error {
		return c.String(http.StatusTeapot, "hello "+c.Param("name")+" ("+c.Path()+")")
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	assert.Equal(t, http.StatusTeapot, rec.Code)
	assert.Equal(t, "hello pinpoint (/hello/:name)", rec.Body.String())
}

// The span is what shows up in Pinpoint, so the request attributes it carries
// have to come from the echo request rather than defaults.
func TestMiddleware_RecordsRequestAttributesOnTheSpan(t *testing.T) {
	pptest.StartAgent(t)

	var tracer pinpoint.Tracer
	e := echo.New()
	e.Use(Middleware())
	e.GET("/hello/:name", func(c *echo.Context) error {
		tracer = pinpoint.TracerFromRequestContext(c.Request())
		return c.NoContent(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil)
	req.Host = "myhost:8080"
	req.RemoteAddr = "10.0.0.1:4242"
	e.ServeHTTP(httptest.NewRecorder(), req)

	span := pptest.SpanOf(t, tracer)
	assert.Equal(t, "/hello/pinpoint", span["RpcName"], "the span is named after the request path, not the route pattern")
	assert.Equal(t, "myhost:8080", span["EndPoint"])
	assert.Equal(t, "10.0.0.1", span["RemoteAddr"])
}

// An echo service is usually one hop of a larger call: the tracing headers the
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
	e := echo.New()
	e.Use(Middleware())
	e.GET("/hello", func(c *echo.Context) error {
		tracer = pinpoint.TracerFromRequestContext(c.Request())
		return nil
	})

	e.ServeHTTP(httptest.NewRecorder(), req)

	require.NotNil(t, tracer)
	assert.Equal(t, caller.TransactionId().String(), tracer.TransactionId().String())
}

// A handler either returns an error - and echo decides the status - or writes
// the response itself. The span has to record what the client actually got in
// both shapes, and fail on the configured error class.
func TestMiddleware_RecordsTheFinalStatus(t *testing.T) {
	tests := []struct {
		name       string
		handler    echo.HandlerFunc
		wantStatus int
		wantFail   bool
	}{
		{
			name:       "a handler that writes its own status",
			handler:    func(c *echo.Context) error { return c.String(http.StatusTeapot, "teapot") },
			wantStatus: http.StatusTeapot,
		},
		{
			name:       "a handler that writes nothing leaves echo's implicit 200",
			handler:    func(c *echo.Context) error { return nil },
			wantStatus: http.StatusOK,
		},
		{
			name:       "an HTTPError is recorded and its status reported",
			handler:    func(c *echo.Context) error { return echo.NewHTTPError(http.StatusNotFound, "gone") },
			wantStatus: http.StatusNotFound,
			wantFail:   true,
		},
		{
			name:       "a plain error becomes a 500",
			handler:    func(c *echo.Context) error { return errors.New("boom") },
			wantStatus: http.StatusInternalServerError,
			wantFail:   true,
		},
		{
			name:       "a handler that writes a 5xx itself",
			handler:    func(c *echo.Context) error { return c.String(http.StatusBadGateway, "bad") },
			wantStatus: http.StatusBadGateway,
			wantFail:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pptest.StartAgent(t)

			var tracer pinpoint.Tracer
			e := echo.New()
			e.Use(Middleware())
			e.GET("/", func(c *echo.Context) error {
				tracer = pinpoint.TracerFromRequestContext(c.Request())
				return tt.handler(c)
			})

			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantFail, pptest.SpanOf(t, tracer)["Err"] != float64(0))
		})
	}
}

// WrapHandler instruments one route instead of the whole router.
func TestWrapHandler_PutsSampledTracerInRequestContext(t *testing.T) {
	pptest.StartAgent(t)

	var tracer pinpoint.Tracer
	e := echo.New()
	e.GET("/wrapped", WrapHandler(func(c *echo.Context) error {
		tracer = pinpoint.TracerFromRequestContext(c.Request())
		return c.NoContent(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wrapped", nil))

	require.NotNil(t, tracer)
	assert.True(t, tracer.IsSampled(), "wrapped handler received an unsampled tracer")
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "/wrapped", pptest.SpanOf(t, tracer)["RpcName"])
}

// A route no handler is registered for is echo's own 404; the middleware still
// wraps it and must not disturb the response.
func TestMiddleware_UnmatchedRoute(t *testing.T) {
	pptest.StartAgent(t)

	e := echo.New()
	e.Use(Middleware())
	e.GET("/hello", func(c *echo.Context) error { return nil })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nowhere", nil))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// The wrapper marks the span failed and re-panics; swallowing the panic would
// turn a crash echo's Recover middleware reports into a silent 200.
func TestMiddleware_PanicPropagates(t *testing.T) {
	pptest.StartAgent(t)

	var tracer pinpoint.Tracer
	e := echo.New()
	e.Use(Middleware())
	e.GET("/boom", func(c *echo.Context) error {
		tracer = pinpoint.TracerFromRequestContext(c.Request())
		panic("boom")
	})

	assert.PanicsWithValue(t, "boom", func() {
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
	}, "the wrapper swallowed the handler panic")

	assert.NotEqual(t, float64(0), pptest.SpanOf(t, tracer)["Err"], "a panicking handler must fail the span")
}

// With no agent running the middleware must be a straight pass-through.
func TestMiddleware_PassesThroughWhenAgentDisabled(t *testing.T) {
	called := false
	e := echo.New()
	e.Use(Middleware())
	e.GET("/", func(c *echo.Context) error {
		called = true
		assert.False(t, pinpoint.TracerFromRequestContext(c.Request()).IsSampled(),
			"a disabled agent produced a sampled tracer")
		return c.NoContent(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.True(t, called, "the handler did not run")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// WrapHandler is the other entry point and has to pass through too, error and
// all.
func TestWrapHandler_PassesThroughWhenAgentDisabled(t *testing.T) {
	e := echo.New()
	e.GET("/boom", WrapHandler(func(c *echo.Context) error {
		return echo.NewHTTPError(http.StatusTeapot, "boom")
	}))

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	assert.Equal(t, http.StatusTeapot, rec.Code, "the handler error must still reach echo's error handler")
}

// v5 does not record the handler function name on a route, so Middleware falls
// back to a fixed span event name. A name set through echo.AddRoute is the one
// case it can still report, and routeName has to tell that apart from
// RouteInfo's "METHOD:/path" default. This also pins that RouteInfo is already
// populated where the middleware reads it.
func Test_routeName(t *testing.T) {
	var got [3]string
	e := echo.New()
	e.GET("/plain", func(c *echo.Context) error { got[0] = routeName(c); return nil })
	_, err := e.AddRoute(echo.Route{
		Method:  http.MethodGet,
		Path:    "/named",
		Name:    "helloHandler",
		Handler: func(c *echo.Context) error { got[1] = routeName(c); return nil },
	})
	require.NoError(t, err)

	// A route explicitly named after RouteInfo's own default must not be
	// mistaken for a real name.
	_, err = e.AddRoute(echo.Route{
		Method:  http.MethodGet,
		Path:    "/default-name",
		Name:    http.MethodGet + ":/default-name",
		Handler: func(c *echo.Context) error { got[2] = routeName(c); return nil },
	})
	require.NoError(t, err)

	for _, path := range []string{"/plain", "/named", "/default-name"} {
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	assert.Equal(t, "echo.HandlerFunc()", got[0], "unnamed route")
	assert.Equal(t, "helloHandler()", got[1], "named route")
	assert.Equal(t, "echo.HandlerFunc()", got[2], "a route named after RouteInfo's default")
}
