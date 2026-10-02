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
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startAgent(t *testing.T) {
	t.Helper()
	config, err := pinpoint.NewConfig(pinpoint.WithAppName("testApp"), pinpoint.WithAgentName("testAgent"))
	require.NoError(t, err)

	agent, err := pinpoint.NewTestAgent(config)
	require.NoError(t, err)
	t.Cleanup(agent.Shutdown)
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
		{name: "a wrapped HTTPError is unwrapped", err: fmt.Errorf("wrapped: %w", echo.NewHTTPError(http.StatusTeapot)), want: http.StatusTeapot},
		{name: "a twice-wrapped HTTPError is still unwrapped", err: fmt.Errorf("wrapped: %w", fmt.Errorf("wrapped: %w", echo.NewHTTPError(http.StatusTeapot))), want: http.StatusTeapot},
		{name: "a wrapped plain error is a server error", err: fmt.Errorf("wrapped: %w", errors.New("boom")), want: http.StatusInternalServerError},
		{name: "an HTTPError built with a message", err: echo.NewHTTPError(http.StatusBadRequest, "bad"), want: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, statusCode(tt.err))
		})
	}
}

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
			startAgent(t)

			e := echo.New()
			calls := 0
			e.HTTPErrorHandler = func(err error, c echo.Context) {
				calls++
				e.DefaultHTTPErrorHandler(err, c)
			}
			var tracer pinpoint.Tracer
			tt.route(e, func(c echo.Context) error {
				tracer = pinpoint.TracerFromRequestContext(c.Request())
				return echo.NewHTTPError(http.StatusTeapot, "boom")
			})

			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

			assert.Equal(t, 1, calls, "HTTPErrorHandler ran more than once for one failed request")
			assert.Equal(t, http.StatusTeapot, rec.Code)
			assert.NotEqual(t, float64(0), spanOf(t, tracer)["Err"], "the handler error must be recorded on the span")
		})
	}
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
	e := echo.New()
	e.GET("/boom", WrapHandler(func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusTeapot)
	}))

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	assert.Equal(t, http.StatusTeapot, rec.Code, "the handler error must still reach echo's error handler")
}

// wrap resolves the span event name into a local, never into its captured
// parameter. echo re-applies Middleware() per request, so a memoized name only
// escapes when one wrapped handler is registered on more than one route - but
// a closure that rewrites its own parameter is also a plain data race between
// concurrent requests, which is what this test pins under -race.
func Test_wrap_ResolvesTheNamePerRequest(t *testing.T) {
	startAgent(t)

	e := echo.New()
	shared := wrap(func(c echo.Context) error { return c.NoContent(http.StatusNoContent) }, "")
	e.GET("/first", shared)
	e.GET("/second", shared)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := "/first"
			if i%2 == 1 {
				path = "/second"
			}
			for j := 0; j < 25; j++ {
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
				assert.Equal(t, http.StatusNoContent, rec.Code)
			}
		}(i)
	}
	wg.Wait()
}

// statusAnnotation reads the recorded HTTP status back out of the span JSON:
// {"key":46,"value":{"Field":{"IntValue":500}}}.
func statusAnnotation(t *testing.T, tracer pinpoint.Tracer) int {
	t.Helper()
	annotations, _ := spanOf(t, tracer)["Annotations"].([]interface{})
	for _, a := range annotations {
		m, _ := a.(map[string]interface{})
		if key, _ := m["key"].(float64); int(key) != pinpoint.AnnotationHttpStatusCode {
			continue
		}
		value, _ := m["value"].(map[string]interface{})
		field, _ := value["Field"].(map[string]interface{})
		n, _ := field["IntValue"].(float64)
		return int(n)
	}
	return 0
}

// A handler that wrote its response and then returned an error: echo's error
// handler leaves a committed response alone, so the wire keeps the status the
// handler wrote and the span must record that one, not the error's.
func TestMiddleware_RecordsTheCommittedStatusOverTheErrors(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	e := echo.New()
	e.Use(Middleware())
	e.GET("/", func(c echo.Context) error {
		tracer = pinpoint.TracerFromRequestContext(c.Request())
		if err := c.String(http.StatusOK, "ok"); err != nil {
			return err
		}
		return errors.New("late failure")
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, http.StatusOK, statusAnnotation(t, tracer), "the recorded status must be the one on the wire")
}
