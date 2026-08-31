package ppfiberv3

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/gofiber/fiber/v3"
	recovermw "github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/valyala/fasthttp"
)

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

func request(t *testing.T, app *fiber.App, method, target string) *http.Response {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(method, target, nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// The wrapper reports the status fiber's ErrorHandler will send, instead of
// invoking that handler itself to read the status off the response.
func Test_statusCode(t *testing.T) {
	if got := statusCode(fiber.NewError(http.StatusNotFound)); got != http.StatusNotFound {
		t.Errorf("statusCode(404) = %d, want 404", got)
	}
	if got := statusCode(errors.New("boom")); got != http.StatusInternalServerError {
		t.Errorf("statusCode(plain error) = %d, want 500", got)
	}
	// fiber.Error is reported through errors.As, so a wrapped one still counts.
	wrapped := fmtWrap(fiber.NewError(http.StatusTeapot))
	if got := statusCode(wrapped); got != http.StatusTeapot {
		t.Errorf("statusCode(wrapped 418) = %d, want 418", got)
	}
}

type wrapErr struct{ err error }

func (e wrapErr) Error() string { return "wrapped: " + e.err.Error() }
func (e wrapErr) Unwrap() error { return e.err }

func fmtWrap(err error) error { return wrapErr{err} }

// The middleware sits in front of every route, so it must leave fiber's own
// behaviour intact: route parameters still resolve and the handler's status and
// body reach the client unchanged.
//
// The route the wrapper reports to URL statistics is read after Next() has
// returned, which only yields the endpoint's pattern because fiber leaves
// c.route on the deepest match instead of restoring the Use route.
func TestMiddleware_PreservesRouting(t *testing.T) {
	startAgent(t)

	var urlStatPath string
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		defer func() { urlStatPath = c.Route().Path }()
		return c.Next()
	})
	app.Use(Middleware())
	app.Get("/hello/:name", func(c fiber.Ctx) error {
		return c.Status(http.StatusTeapot).SendString("hello " + c.Params("name") + " (" + c.Route().Path + ")")
	})

	resp := request(t, app, http.MethodGet, "/hello/pinpoint")

	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusTeapot)
	}
	if got := readBody(t, resp); got != "hello pinpoint (/hello/:name)" {
		t.Errorf("body = %q", got)
	}
	if urlStatPath != "/hello/:name" {
		t.Errorf("route seen after Next() = %q, want %q", urlStatPath, "/hello/:name")
	}
}

// The handler reads its tracer out of the request context.
func TestMiddleware_PutsSampledTracerInContext(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	app := fiber.New()
	app.Use(Middleware())
	app.Get("/", func(c fiber.Ctx) error {
		tracer = pinpoint.FromContext(c.Context())
		return nil
	})

	request(t, app, http.MethodGet, "/")

	if tracer == nil {
		t.Fatal("no tracer in the handler's context")
	}
	if !tracer.IsSampled() {
		t.Error("handler received an unsampled tracer")
	}
}

// The context is shared with whatever middleware ran earlier. Replacing it with
// a fresh background context - instead of deriving from it - discarded the
// values and deadlines the rest of the handler chain depends on.
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

	request(t, app, http.MethodGet, "/")

	if gotUser != "from-auth-middleware" {
		t.Errorf("earlier middleware's context value = %v, want %q", gotUser, "from-auth-middleware")
	}
	if gotTracer == nil || !gotTracer.IsSampled() {
		t.Error("the tracer did not reach the handler's context")
	}
}

// WrapHandler instruments one route instead of the whole app.
func TestWrapHandler_PutsSampledTracerInContext(t *testing.T) {
	startAgent(t)

	var sampled bool
	app := fiber.New()
	app.Get("/wrapped", WrapHandler(func(c fiber.Ctx) error {
		sampled = pinpoint.FromContext(c.Context()).IsSampled()
		return c.SendStatus(http.StatusNoContent)
	}))

	resp := request(t, app, http.MethodGet, "/wrapped")

	if !sampled {
		t.Error("wrapped handler received an unsampled tracer")
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
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
	app.Get("/boom", func(_ fiber.Ctx) error { return fiber.NewError(http.StatusTeapot) })

	resp := request(t, app, http.MethodGet, "/boom")

	if calls != 1 {
		t.Errorf("ErrorHandler ran %d times, want 1", calls)
	}
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusTeapot)
	}
}

// The wrapper marks the span failed and re-panics; swallowing the panic would
// turn a crash fiber's recover middleware reports into a silent 200.
func TestMiddleware_RepanicsIntoTheRecoverMiddleware(t *testing.T) {
	startAgent(t)

	app := fiber.New()
	app.Use(recovermw.New())
	app.Use(Middleware())
	app.Get("/boom", func(_ fiber.Ctx) error { panic("boom") })

	resp := request(t, app, http.MethodGet, "/boom")

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
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
		if pinpoint.FromContext(c.Context()).IsSampled() {
			t.Error("a disabled agent produced a sampled tracer")
		}
		return nil
	})

	resp := request(t, app, http.MethodGet, "/")

	if !called {
		t.Fatal("the handler did not run")
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// fiber stores headers as bytes in fasthttp's multi-map. These adapters are
// what the agent reads inbound headers and cookies through, so a mistake here
// silently drops every recorded value rather than failing loudly.
func Test_headerAndCookieAdapters(t *testing.T) {
	var req fasthttp.Request
	req.Header.Set("X-Trace", "abc")
	req.Header.SetCookie("first", "1")
	req.Header.SetCookie("second", "2")

	h := fiberRequestHeader{&req.Header}
	if got := h.Get("x-trace"); got != "abc" {
		t.Errorf("Get(x-trace) = %q, want %q", got, "abc")
	}
	if got := h.Get("X-Absent"); got != "" {
		t.Errorf("Get(X-Absent) = %q, want empty", got)
	}
	if got := h.Values("X-Trace"); len(got) != 1 || got[0] != "abc" {
		t.Errorf("Values(X-Trace) = %q, want [abc]", got)
	}

	visited := false
	h.VisitAll(func(name string, values []string) {
		if name == "X-Trace" && len(values) == 1 && values[0] == "abc" {
			visited = true
		}
	})
	if !visited {
		t.Error("VisitAll did not report X-Trace")
	}

	var cookies []string
	fiberCookie{&req.Header}.VisitAll(func(name, value string) {
		cookies = append(cookies, name+"="+value)
	})
	sort.Strings(cookies)
	if len(cookies) != 2 || cookies[0] != "first=1" || cookies[1] != "second=2" {
		t.Errorf("cookie VisitAll gave %q, want [first=1 second=2]", cookies)
	}

	var resp fasthttp.Response
	resp.Header.Set("X-Result", "ok")
	if got := (fiberResponseHeader{&resp.Header}).Values("X-Result"); len(got) != 1 || got[0] != "ok" {
		t.Errorf("response Values(X-Result) = %q, want [ok]", got)
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
