package ppfiberv3

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	recovermw "github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
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

func request(t *testing.T, app *fiber.App, req *http.Request) *http.Response {
	t.Helper()
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func get(t *testing.T, app *fiber.App, target string) *http.Response {
	t.Helper()
	return request(t, app, httptest.NewRequest(http.MethodGet, target, nil))
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

type wrapErr struct{ err error }

func (e wrapErr) Error() string { return "wrapped: " + e.err.Error() }
func (e wrapErr) Unwrap() error { return e.err }

// The wrapper reports the status fiber's ErrorHandler will send, instead of
// invoking that handler itself to read the status off the response.
func Test_statusCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "a fiber.Error carries its own status", err: fiber.NewError(http.StatusNotFound), want: http.StatusNotFound},
		{name: "a plain error is a server error", err: errors.New("boom"), want: http.StatusInternalServerError},
		{name: "a wrapped fiber.Error is unwrapped", err: wrapErr{fiber.NewError(http.StatusTeapot)}, want: http.StatusTeapot},
		{name: "a twice-wrapped fiber.Error is still unwrapped", err: wrapErr{wrapErr{fiber.NewError(http.StatusTeapot)}}, want: http.StatusTeapot},
		{name: "a wrapped plain error is a server error", err: wrapErr{errors.New("boom")}, want: http.StatusInternalServerError},
		{name: "a fiber.Error built with a message", err: fiber.NewError(http.StatusBadRequest, "bad"), want: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, statusCode(tt.err))
		})
	}
}

// The middleware sits in front of every route, so it must leave fiber's own
// behaviour intact: route parameters still resolve and the handler's status and
// body reach the client unchanged.
func TestMiddleware_PreservesRouting(t *testing.T) {
	startAgent(t)

	app := fiber.New()
	app.Use(Middleware())
	app.Get("/hello/:name", func(c fiber.Ctx) error {
		return c.Status(http.StatusTeapot).SendString("hello " + c.Params("name") + " (" + c.Route().Path + ")")
	})

	resp := get(t, app, "/hello/pinpoint")

	assert.Equal(t, http.StatusTeapot, resp.StatusCode)
	assert.Equal(t, "hello pinpoint (/hello/:name)", readBody(t, resp))
}

// The handler reads its tracer out of the request context.
func TestMiddleware_PutsSampledTracerInRequestContext(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	app := fiber.New()
	app.Use(Middleware())
	app.Get("/", func(c fiber.Ctx) error {
		tracer = pinpoint.FromContext(c.Context())
		return nil
	})

	get(t, app, "/")

	require.NotNil(t, tracer, "no tracer in the handler's request context")
	assert.True(t, tracer.IsSampled(), "handler received an unsampled tracer")
	assert.NotEmpty(t, tracer.TransactionId().String())
}

// The span attributes are read straight off the fasthttp request fiber owns;
// the wrapper never converts it to a net/http request.
func TestMiddleware_RecordsRequestAttributesOnTheSpan(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	app := fiber.New()
	app.Use(Middleware())
	app.Get("/hello/:name", func(c fiber.Ctx) error {
		tracer = pinpoint.FromContext(c.Context())
		return nil
	})

	req := httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil)
	req.Host = "myhost:8080"
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.2")
	request(t, app, req)

	span := spanOf(t, tracer)
	assert.Equal(t, "/hello/pinpoint", span["RpcName"], "the span is named after the request path, not the route pattern")
	assert.Equal(t, "myhost:8080", span["EndPoint"])
	assert.Equal(t, "203.0.113.7", span["RemoteAddr"], "X-Forwarded-For must win over the transport peer address")
}

// A fiber service is usually one hop of a larger call: the tracing headers the
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
	app := fiber.New()
	app.Use(Middleware())
	app.Get("/hello", func(c fiber.Ctx) error {
		tracer = pinpoint.FromContext(c.Context())
		return nil
	})

	request(t, app, req)

	require.NotNil(t, tracer)
	assert.Equal(t, caller.TransactionId().String(), tracer.TransactionId().String())
}

// The request context is shared with whatever middleware ran earlier. Replacing
// it with a fresh background context - instead of deriving from it - discarded
// the values and deadlines the rest of the handler chain depends on.
func TestMiddleware_KeepsExistingContextValues(t *testing.T) {
	startAgent(t)

	type ctxKey struct{}

	var (
		gotUser   interface{}
		gotTracer pinpoint.Tracer
	)
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.SetContext(context.WithValue(c.Context(), ctxKey{}, "from-auth-middleware"))
		return c.Next()
	})
	app.Use(Middleware())
	app.Get("/", func(c fiber.Ctx) error {
		gotUser = c.Context().Value(ctxKey{})
		gotTracer = pinpoint.FromContext(c.Context())
		return nil
	})

	get(t, app, "/")

	assert.Equal(t, "from-auth-middleware", gotUser, "an earlier middleware's context value was discarded")
	require.NotNil(t, gotTracer)
	assert.True(t, gotTracer.IsSampled(), "the tracer did not reach the handler's request context")
}

// A handler either returns an error - and fiber decides the status - or writes
// the response itself. The span has to record what the client actually got in
// both shapes.
func TestMiddleware_RecordsTheFinalStatus(t *testing.T) {
	tests := []struct {
		name       string
		handler    fiber.Handler
		wantStatus int
		wantFail   bool
	}{
		{
			name:       "a handler that writes its own status",
			handler:    func(c fiber.Ctx) error { return c.SendStatus(http.StatusTeapot) },
			wantStatus: http.StatusTeapot,
		},
		{
			name:       "a handler that writes nothing leaves fiber's implicit 200",
			handler:    func(c fiber.Ctx) error { return nil },
			wantStatus: http.StatusOK,
		},
		{
			name:       "a fiber.Error is recorded and its status reported",
			handler:    func(c fiber.Ctx) error { return fiber.NewError(http.StatusNotFound) },
			wantStatus: http.StatusNotFound,
			wantFail:   true,
		},
		{
			name:       "a plain error becomes a 500",
			handler:    func(c fiber.Ctx) error { return errors.New("boom") },
			wantStatus: http.StatusInternalServerError,
			wantFail:   true,
		},
		{
			name:       "a handler that writes a 5xx itself",
			handler:    func(c fiber.Ctx) error { return c.SendStatus(http.StatusBadGateway) },
			wantStatus: http.StatusBadGateway,
			wantFail:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			startAgent(t)

			var tracer pinpoint.Tracer
			app := fiber.New()
			app.Use(Middleware())
			app.Get("/", func(c fiber.Ctx) error {
				tracer = pinpoint.FromContext(c.Context())
				return tt.handler(c)
			})

			resp := get(t, app, "/")

			assert.Equal(t, tt.wantStatus, resp.StatusCode)
			assert.Equal(t, tt.wantFail, spanOf(t, tracer)["Err"] != float64(0))
		})
	}
}

// WrapHandler instruments one route instead of the whole app.
func TestWrapHandler_PutsSampledTracerInRequestContext(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	app := fiber.New()
	app.Get("/wrapped", WrapHandler(func(c fiber.Ctx) error {
		tracer = pinpoint.FromContext(c.Context())
		return c.SendStatus(http.StatusNoContent)
	}))

	resp := get(t, app, "/wrapped")

	require.NotNil(t, tracer)
	assert.True(t, tracer.IsSampled(), "wrapped handler received an unsampled tracer")
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, "/wrapped", spanOf(t, tracer)["RpcName"])
}

// An error a wrapped handler returns has to be recorded on the span and still
// reach fiber's error handler.
func TestWrapHandler_RecordsTheHandlerError(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	app := fiber.New()
	app.Get("/boom", WrapHandler(func(c fiber.Ctx) error {
		tracer = pinpoint.FromContext(c.Context())
		return fiber.NewError(http.StatusTeapot, "boom")
	}))

	resp := get(t, app, "/boom")

	assert.Equal(t, http.StatusTeapot, resp.StatusCode)
	assert.NotEqual(t, float64(0), spanOf(t, tracer)["Err"], "the handler error must be recorded on the span")
}

// A handler that returns an error must have fiber's ErrorHandler run once - by
// fiber, from the returned error - not once by the wrapper and again by fiber.
func TestMiddleware_RunsErrorHandlerOnce(t *testing.T) {
	startAgent(t)

	calls := 0
	app := fiber.New(fiber.Config{
		ErrorHandler: func(c fiber.Ctx, err error) error {
			calls++
			return fiber.DefaultErrorHandler(c, err)
		},
	})
	app.Use(Middleware())
	app.Get("/boom", func(c fiber.Ctx) error { return fiber.NewError(http.StatusTeapot) })

	resp := get(t, app, "/boom")

	assert.Equal(t, 1, calls, "ErrorHandler ran more than once for one failed request")
	assert.Equal(t, http.StatusTeapot, resp.StatusCode)
}

// A path no route matches is fiber's own 404; the middleware still wraps it and
// must not disturb the response.
func TestMiddleware_UnmatchedRoute(t *testing.T) {
	startAgent(t)

	app := fiber.New()
	app.Use(Middleware())
	app.Get("/hello", func(c fiber.Ctx) error { return nil })

	resp := get(t, app, "/nowhere")

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// The wrapper marks the span failed and re-panics; swallowing the panic would
// turn a crash fiber's recover middleware reports into a silent 200.
func TestMiddleware_RepanicsIntoTheRecoverMiddleware(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	app := fiber.New()
	app.Use(recovermw.New())
	app.Use(Middleware())
	app.Get("/boom", func(c fiber.Ctx) error {
		tracer = pinpoint.FromContext(c.Context())
		panic("boom")
	})

	resp := get(t, app, "/boom")

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	assert.NotEqual(t, float64(0), spanOf(t, tracer)["Err"], "a panicking handler must fail the span")
}

// With no agent running the middleware must be a straight pass-through.
func TestMiddleware_PassesThroughWhenAgentDisabled(t *testing.T) {
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

	called := false
	app := fiber.New()
	app.Use(Middleware())
	app.Get("/", func(c fiber.Ctx) error {
		called = true
		assert.False(t, pinpoint.FromContext(c.Context()).IsSampled(),
			"a disabled agent produced a sampled tracer")
		return nil
	})

	resp := get(t, app, "/")

	require.True(t, called, "the handler did not run")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// WrapHandler is the other entry point and has to pass through too, error and
// all.
func TestWrapHandler_PassesThroughWhenAgentDisabled(t *testing.T) {
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

	app := fiber.New()
	app.Get("/boom", WrapHandler(func(c fiber.Ctx) error {
		return fiber.NewError(http.StatusTeapot, "boom")
	}))

	resp := get(t, app, "/boom")

	assert.Equal(t, http.StatusTeapot, resp.StatusCode, "the handler error must still reach fiber's error handler")
}

// fiber stores headers as bytes in fasthttp's multi-map. These adapters are
// what the agent reads inbound headers and cookies through, so a mistake here
// silently drops every recorded value rather than failing loudly.
func Test_headerAndCookieAdapters(t *testing.T) {
	var req fasthttp.Request
	req.Header.Set("X-Trace", "abc")
	req.Header.Add("X-Multi", "one")
	req.Header.Add("X-Multi", "two")
	req.Header.SetCookie("first", "1")
	req.Header.SetCookie("second", "2")

	h := fiberRequestHeader{&req.Header}
	assert.Equal(t, "abc", h.Get("x-trace"), "header names are case-insensitive")
	assert.Equal(t, "", h.Get("X-Absent"))
	assert.Equal(t, []string{"abc"}, h.Values("X-Trace"))
	assert.Equal(t, []string{"one"}, h.Values("X-Multi"), "Peek returns the first value only")

	visited := map[string][]string{}
	h.VisitAll(func(name string, values []string) {
		visited[name] = append(visited[name], values...)
	})
	assert.Equal(t, []string{"abc"}, visited["X-Trace"])
	assert.Len(t, visited["X-Multi"], 2, "VisitAll must report both values of a repeated header")

	cookies := map[string]string{}
	fiberCookie{&req.Header}.VisitAll(func(name, value string) { cookies[name] = value })
	assert.Equal(t, map[string]string{"first": "1", "second": "2"}, cookies)

	var resp fasthttp.Response
	resp.Header.Set("X-Result", "ok")
	rh := fiberResponseHeader{&resp.Header}
	assert.Equal(t, []string{"ok"}, rh.Values("X-Result"))
	assert.Equal(t, []string{""}, rh.Values("X-Absent"), "an absent response header reads as one empty value")

	resVisited := map[string][]string{}
	rh.VisitAll(func(name string, values []string) { resVisited[name] = values })
	assert.Equal(t, []string{"ok"}, resVisited["X-Result"])
}
