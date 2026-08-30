package ppecho

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/labstack/echo"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// The wrapper reports the status echo's HTTPErrorHandler will send, instead of
// invoking that handler itself to read the status off the response.
func Test_statusCode(t *testing.T) {
	if got := statusCode(echo.NewHTTPError(http.StatusNotFound)); got != http.StatusNotFound {
		t.Errorf("statusCode(404) = %d, want 404", got)
	}
	if got := statusCode(errors.New("boom")); got != http.StatusInternalServerError {
		t.Errorf("statusCode(plain error) = %d, want 500", got)
	}
}

// A handler that returns an error must have echo's HTTPErrorHandler run once -
// by echo, from the returned error - not once by the wrapper and again by echo.
func Test_wrapHandler_RunsErrorHandlerOnce(t *testing.T) {
	config, err := pinpoint.NewConfig(pinpoint.WithAppName("testApp"), pinpoint.WithAgentId("testAgent"))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := pinpoint.NewTestAgent(config, t)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Shutdown()

	e := echo.New()
	calls := 0
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		calls++
		e.DefaultHTTPErrorHandler(err, c)
	}
	e.GET("/boom", WrapHandler(func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusTeapot)
	}))

	req, _ := http.NewRequest(http.MethodGet, "/boom", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if calls != 1 {
		t.Errorf("HTTPErrorHandler ran %d times, want 1", calls)
	}
	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusTeapot)
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

type wrapErr struct{ err error }

func (e wrapErr) Error() string { return "wrapped: " + e.err.Error() }
func (e wrapErr) Unwrap() error { return e.err }

// echo's own middleware wrap errors rather than replacing them, so the status
// has to be read through the error chain.
func Test_statusCodeUnwrapsTheError(t *testing.T) {
	if got := statusCode(wrapErr{echo.NewHTTPError(http.StatusTeapot)}); got != http.StatusTeapot {
		t.Errorf("statusCode(wrapped 418) = %d, want 418", got)
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

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
	if want := "hello pinpoint (/hello/:name)"; rec.Body.String() != want {
		t.Errorf("body = %q, want %q", rec.Body.String(), want)
	}
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

	if tracer == nil {
		t.Fatal("no tracer in the handler's request context")
	}
	if !tracer.IsSampled() {
		t.Error("handler received an unsampled tracer")
	}
}

// WrapHandler instruments one route instead of the whole router.
func TestWrapHandler_PutsSampledTracerInRequestContext(t *testing.T) {
	startAgent(t)

	var sampled bool
	e := echo.New()
	e.GET("/wrapped", WrapHandler(func(c echo.Context) error {
		sampled = pinpoint.TracerFromRequestContext(c.Request()).IsSampled()
		return c.NoContent(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wrapped", nil))

	if !sampled {
		t.Error("wrapped handler received an unsampled tracer")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
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

	if calls != 1 {
		t.Errorf("HTTPErrorHandler ran %d times, want 1", calls)
	}
	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
}

// The wrapper marks the span failed and re-panics; swallowing the panic would
// turn a crash echo's Recover middleware reports into a silent 200.
func TestMiddleware_PanicPropagates(t *testing.T) {
	startAgent(t)

	e := echo.New()
	e.Use(Middleware())
	e.GET("/boom", func(c echo.Context) error { panic("boom") })

	defer func() {
		if recover() == nil {
			t.Error("the wrapper swallowed the handler panic")
		}
	}()
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
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
		if pinpoint.TracerFromRequestContext(c.Request()).IsSampled() {
			t.Error("a disabled agent produced a sampled tracer")
		}
		return c.NoContent(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if !called {
		t.Fatal("the handler did not run")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
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
				if rec.Code != http.StatusOK {
					t.Errorf("status = %d, want 200", rec.Code)
				}

				rec = httptest.NewRecorder()
				e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/widgets", nil))
				if rec.Code != http.StatusCreated {
					t.Errorf("status = %d, want 201", rec.Code)
				}
			}
		}()
	}
	wg.Wait()
}
