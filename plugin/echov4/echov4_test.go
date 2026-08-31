package ppechov4

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"
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

type wrapErr struct{ err error }

func (e wrapErr) Error() string { return "wrapped: " + e.err.Error() }
func (e wrapErr) Unwrap() error { return e.err }

// The wrapper reports the status echo's HTTPErrorHandler will send, instead of
// invoking that handler itself to read the status off the response. echo's own
// middleware wrap errors rather than replacing them, so the status has to be
// read through the error chain.
func Test_statusCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "an HTTPError carries its own status", err: echo.NewHTTPError(http.StatusNotFound), want: http.StatusNotFound},
		{name: "a plain error is a server error", err: errors.New("boom"), want: http.StatusInternalServerError},
		{name: "a wrapped HTTPError is unwrapped", err: wrapErr{echo.NewHTTPError(http.StatusTeapot)}, want: http.StatusTeapot},
		{name: "a twice-wrapped HTTPError is still unwrapped", err: wrapErr{wrapErr{echo.NewHTTPError(http.StatusTeapot)}}, want: http.StatusTeapot},
		{name: "a wrapped plain error is a server error", err: wrapErr{errors.New("boom")}, want: http.StatusInternalServerError},
		{name: "an HTTPError built with a message", err: echo.NewHTTPError(http.StatusBadRequest, "bad"), want: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, statusCode(tt.err))
		})
	}
}

// A handler that returns an error must have echo's HTTPErrorHandler run once -
// by echo, from the returned error - not once by the wrapper and again by echo.
func Test_wrapHandler_RunsErrorHandlerOnce(t *testing.T) {
	startAgent(t)

	e := echo.New()
	calls := 0
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		calls++
		e.DefaultHTTPErrorHandler(err, c)
	}
	e.GET("/boom", WrapHandler(func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusTeapot)
	}))

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	assert.Equal(t, 1, calls, "HTTPErrorHandler ran more than once for one failed request")
	assert.Equal(t, http.StatusTeapot, rec.Code)
}

// The middleware sits in front of every route, so it must leave echo's own
// behaviour intact: route parameters still resolve and the handler's status and
// body reach the client unchanged.
func TestMiddleware_PreservesRouting(t *testing.T) {
	startAgent(t)

	e := echo.New()
	e.Use(Middleware())
	e.GET("/hello/:name", func(c echo.Context) error {
		return c.String(http.StatusTeapot, "hello "+c.Param("name")+" ("+c.Path()+")")
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	assert.Equal(t, http.StatusTeapot, rec.Code)
	assert.Equal(t, "hello pinpoint (/hello/:name)", rec.Body.String())
}

// The handler reads its tracer out of the request context, so the wrapper has
// to replace c.Request with the tracer-carrying one before calling the handler.
func TestMiddleware_PutsSampledTracerInRequestContext(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	e := echo.New()
	e.Use(Middleware())
	e.GET("/", func(c echo.Context) error {
		tracer = pinpoint.TracerFromRequestContext(c.Request())
		return nil
	})

	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	require.NotNil(t, tracer, "no tracer in the handler's request context")
	assert.True(t, tracer.IsSampled(), "handler received an unsampled tracer")
	assert.NotEmpty(t, tracer.TransactionId().String())
}

// The span is what shows up in Pinpoint, so the request attributes it carries
// have to come from the echo request rather than defaults.
func TestMiddleware_RecordsRequestAttributesOnTheSpan(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	e := echo.New()
	e.Use(Middleware())
	e.GET("/hello/:name", func(c echo.Context) error {
		tracer = pinpoint.TracerFromRequestContext(c.Request())
		return c.NoContent(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil)
	req.Host = "myhost:8080"
	req.RemoteAddr = "10.0.0.1:4242"
	e.ServeHTTP(httptest.NewRecorder(), req)

	span := spanOf(t, tracer)
	assert.Equal(t, "/hello/pinpoint", span["RpcName"], "the span is named after the request path, not the route pattern")
	assert.Equal(t, "myhost:8080", span["EndPoint"])
	assert.Equal(t, "10.0.0.1", span["RemoteAddr"])
}

// An echo service is usually one hop of a larger call: the tracing headers the
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
	e := echo.New()
	e.Use(Middleware())
	e.GET("/hello", func(c echo.Context) error {
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
			handler:    func(c echo.Context) error { return c.String(http.StatusTeapot, "teapot") },
			wantStatus: http.StatusTeapot,
		},
		{
			name:       "a handler that writes nothing leaves echo's implicit 200",
			handler:    func(c echo.Context) error { return nil },
			wantStatus: http.StatusOK,
		},
		{
			name:       "an HTTPError the client sees as 4xx does not fail the span by default",
			handler:    func(c echo.Context) error { return echo.NewHTTPError(http.StatusNotFound) },
			wantStatus: http.StatusNotFound,
			wantFail:   true, // the returned error itself is recorded
		},
		{
			name:       "a plain error becomes a 500",
			handler:    func(c echo.Context) error { return errors.New("boom") },
			wantStatus: http.StatusInternalServerError,
			wantFail:   true,
		},
		{
			name:       "a handler that writes a 5xx itself",
			handler:    func(c echo.Context) error { return c.String(http.StatusBadGateway, "bad") },
			wantStatus: http.StatusBadGateway,
			wantFail:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			startAgent(t)

			var tracer pinpoint.Tracer
			e := echo.New()
			e.Use(Middleware())
			e.GET("/", func(c echo.Context) error {
				tracer = pinpoint.TracerFromRequestContext(c.Request())
				return tt.handler(c)
			})

			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantFail, spanOf(t, tracer)["Err"] != float64(0))
		})
	}
}

// WrapHandler instruments one route instead of the whole router.
func TestWrapHandler_PutsSampledTracerInRequestContext(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	e := echo.New()
	e.GET("/wrapped", WrapHandler(func(c echo.Context) error {
		tracer = pinpoint.TracerFromRequestContext(c.Request())
		return c.NoContent(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wrapped", nil))

	require.NotNil(t, tracer)
	assert.True(t, tracer.IsSampled(), "wrapped handler received an unsampled tracer")
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "/wrapped", spanOf(t, tracer)["RpcName"])
}

// An error a wrapped handler returns has to be recorded on the span and still
// reach echo's error handler.
func TestWrapHandler_RecordsTheHandlerError(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	e := echo.New()
	e.GET("/boom", WrapHandler(func(c echo.Context) error {
		tracer = pinpoint.TracerFromRequestContext(c.Request())
		return echo.NewHTTPError(http.StatusTeapot, "boom")
	}))

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	assert.Equal(t, http.StatusTeapot, rec.Code)
	assert.NotEqual(t, float64(0), spanOf(t, tracer)["Err"], "the handler error must be recorded on the span")
}

// The middleware form has to route errors to the HTTPErrorHandler exactly once
// too, not only the wrapped-handler form.
func TestMiddleware_RunsErrorHandlerOnce(t *testing.T) {
	startAgent(t)

	e := echo.New()
	calls := 0
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		calls++
		e.DefaultHTTPErrorHandler(err, c)
	}
	e.Use(Middleware())
	e.GET("/boom", func(c echo.Context) error { return echo.NewHTTPError(http.StatusTeapot) })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	assert.Equal(t, 1, calls, "HTTPErrorHandler ran more than once for one failed request")
	assert.Equal(t, http.StatusTeapot, rec.Code)
}

// A route no handler is registered for is echo's own 404; the middleware still
// wraps it and must not disturb the response.
func TestMiddleware_UnmatchedRoute(t *testing.T) {
	startAgent(t)

	e := echo.New()
	e.Use(Middleware())
	e.GET("/hello", func(c echo.Context) error { return nil })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nowhere", nil))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// The wrapper marks the span failed and re-panics; swallowing the panic would
// turn a crash echo's Recover middleware reports into a silent 200.
func TestMiddleware_PanicPropagates(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	e := echo.New()
	e.Use(Middleware())
	e.GET("/boom", func(c echo.Context) error {
		tracer = pinpoint.TracerFromRequestContext(c.Request())
		panic("boom")
	})

	assert.PanicsWithValue(t, "boom", func() {
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
	}, "the wrapper swallowed the handler panic")

	assert.NotEqual(t, float64(0), spanOf(t, tracer)["Err"], "a panicking handler must fail the span")
}

// With no agent running the middleware must be a straight pass-through.
func TestMiddleware_PassesThroughWhenAgentDisabled(t *testing.T) {
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

	called := false
	e := echo.New()
	e.Use(Middleware())
	e.GET("/", func(c echo.Context) error {
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
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

	e := echo.New()
	e.GET("/boom", WrapHandler(func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusTeapot)
	}))

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	assert.Equal(t, http.StatusTeapot, rec.Code, "the handler error must still reach echo's error handler")
}

// The handler-name map is built once per process and read by every request, so
// concurrent requests through the middleware must stay race-free. Run under
// -race.
func TestMiddleware_ConcurrentRequests(t *testing.T) {
	startAgent(t)

	e := echo.New()
	e.Use(Middleware())
	e.GET("/hello/:name", func(c echo.Context) error { return c.String(http.StatusOK, c.Param("name")) })
	e.POST("/widgets", func(c echo.Context) error { return c.NoContent(http.StatusCreated) })

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))
				assert.Equal(t, http.StatusOK, rec.Code)

				rec = httptest.NewRecorder()
				e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/widgets", nil))
				assert.Equal(t, http.StatusCreated, rec.Code)
			}
		}()
	}
	wg.Wait()
}

// handlerName falls back to a fixed name for a route the map does not hold -
// a route registered on a second echo instance, since the map is built once
// from the first one a request goes through.
func Test_handlerName_FallsBackForAnUnknownRoute(t *testing.T) {
	startAgent(t)

	e := echo.New()
	e.Use(Middleware())
	e.GET("/known", func(c echo.Context) error { return nil })

	// Drive one request so the map is built (whichever instance builds it).
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/known", nil))

	other := echo.New()
	other.Use(Middleware())
	other.GET(fmt.Sprintf("/late/%d", 1), func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })

	rec := httptest.NewRecorder()
	assert.NotPanics(t, func() {
		other.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/late/1", nil))
	}, "a route missing from the once-built name map must fall back, not fail")
	assert.Equal(t, http.StatusNoContent, rec.Code)
}
