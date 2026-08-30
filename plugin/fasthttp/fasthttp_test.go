package ppfasthttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sort"
	"testing"

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

func newRequestCtx(method, uri string) *fasthttp.RequestCtx {
	var req fasthttp.Request
	req.Header.SetMethod(method)
	req.SetRequestURI(uri)

	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&req, &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 12345}, nil)
	return ctx
}

// fasthttp stores headers as bytes in its own multi-map. These adapters are
// what the agent reads inbound headers through, so a mistake here silently
// drops every recorded header rather than failing loudly.
func Test_reqHeader(t *testing.T) {
	ctx := newRequestCtx(http.MethodGet, "http://localhost/hello")
	ctx.Request.Header.Set("X-Trace", "abc")
	ctx.Request.Header.Add("X-Multi", "one")
	ctx.Request.Header.Add("X-Multi", "two")
	h := reqHeader{&ctx.Request.Header}

	if got := h.Get("X-Trace"); got != "abc" {
		t.Errorf("Get(X-Trace) = %q, want %q", got, "abc")
	}
	// Header names are case-insensitive.
	if got := h.Get("x-trace"); got != "abc" {
		t.Errorf("Get(x-trace) = %q, want %q", got, "abc")
	}
	if got := h.Get("X-Absent"); got != "" {
		t.Errorf("Get(X-Absent) = %q, want empty", got)
	}
	if got := h.Values("X-Trace"); len(got) != 1 || got[0] != "abc" {
		t.Errorf("Values(X-Trace) = %q, want [abc]", got)
	}
	// Peek returns the first value only, so a repeated header reports one value.
	if got := h.Values("X-Multi"); len(got) != 1 || got[0] != "one" {
		t.Errorf("Values(X-Multi) = %q, want [one]", got)
	}

	visited := map[string][]string{}
	h.VisitAll(func(name string, values []string) {
		visited[name] = append(visited[name], values...)
	})
	if got := visited["X-Trace"]; len(got) != 1 || got[0] != "abc" {
		t.Errorf("VisitAll gave X-Trace = %q, want [abc]", got)
	}
	if got := visited["X-Multi"]; len(got) != 2 {
		t.Errorf("VisitAll gave X-Multi = %q, want both values", got)
	}
}

func Test_resHeader(t *testing.T) {
	ctx := newRequestCtx(http.MethodGet, "http://localhost/hello")
	ctx.Response.Header.Set("X-Result", "ok")
	h := resHeader{&ctx.Response.Header}

	if got := h.Values("X-Result"); len(got) != 1 || got[0] != "ok" {
		t.Errorf("Values(X-Result) = %q, want [ok]", got)
	}
	if got := h.Values("X-Absent"); len(got) != 1 || got[0] != "" {
		t.Errorf("Values(X-Absent) = %q, want [\"\"]", got)
	}

	found := false
	h.VisitAll(func(name string, values []string) {
		if name == "X-Result" && len(values) == 1 && values[0] == "ok" {
			found = true
		}
	})
	if !found {
		t.Error("VisitAll did not report X-Result")
	}
}

// Cookies live in the Cookie request header; the adapter has to split them
// into pairs so the cookie recorder sees names, not one blob.
func Test_cookie(t *testing.T) {
	ctx := newRequestCtx(http.MethodGet, "http://localhost/hello")
	ctx.Request.Header.SetCookie("first", "1")
	ctx.Request.Header.SetCookie("second", "2")

	var got []string
	cookie{&ctx.Request.Header}.VisitAll(func(name, value string) {
		got = append(got, name+"="+value)
	})
	sort.Strings(got)

	if len(got) != 2 || got[0] != "first=1" || got[1] != "second=2" {
		t.Errorf("VisitAll gave %q, want [first=1 second=2]", got)
	}
}

// The handler reads its tracer out of the user value the wrapper stores, so
// that value must be a context carrying a sampled tracer.
func TestWrapHandler_PutsSampledTracerInUserValue(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	h := WrapHandler(func(ctx *fasthttp.RequestCtx) {
		tracer = pinpoint.FromContext(ctx.UserValue(CtxKey).(context.Context))
		ctx.SetStatusCode(http.StatusTeapot)
		ctx.SetBodyString("hello")
	}, "/hello/{name}")

	ctx := newRequestCtx(http.MethodGet, "http://localhost/hello/pinpoint")
	h(ctx)

	if tracer == nil {
		t.Fatal("no tracer in the handler's user value")
	}
	if !tracer.IsSampled() {
		t.Error("handler received an unsampled tracer")
	}
	if ctx.Response.StatusCode() != http.StatusTeapot {
		t.Errorf("status = %d, want %d", ctx.Response.StatusCode(), http.StatusTeapot)
	}
	if got := string(ctx.Response.Body()); got != "hello" {
		t.Errorf("body = %q, want %q", got, "hello")
	}
}

// The route pattern is optional; without it the wrapper skips URL statistics
// and still traces the call.
func TestWrapHandler_WithoutARoutePattern(t *testing.T) {
	startAgent(t)

	called := false
	h := WrapHandler(func(ctx *fasthttp.RequestCtx) { called = true })

	h(newRequestCtx(http.MethodGet, "http://localhost/hello"))

	if !called {
		t.Error("the handler did not run")
	}
}

// The wrapper marks the span failed and re-panics; swallowing the panic would
// turn a crash fasthttp's server reports into a silent 200.
func TestWrapHandler_PanicPropagates(t *testing.T) {
	startAgent(t)

	h := WrapHandler(func(*fasthttp.RequestCtx) { panic("boom") })

	defer func() {
		if recover() == nil {
			t.Error("the wrapper swallowed the handler panic")
		}
	}()
	h(newRequestCtx(http.MethodGet, "http://localhost/boom"))
}

// With no agent running the wrapper must be a straight pass-through, and must
// not leave a user value the handler would type-assert on.
func TestWrapHandler_PassesThroughWhenAgentDisabled(t *testing.T) {
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

	called := false
	h := WrapHandler(func(ctx *fasthttp.RequestCtx) {
		called = true
		if ctx.UserValue(CtxKey) != nil {
			t.Error("a disabled agent still stored a tracer context")
		}
	})

	h(newRequestCtx(http.MethodGet, "http://localhost/hello"))

	if !called {
		t.Fatal("the handler did not run")
	}
}

// DoClient is what links the caller's span to the callee's, so it has to
// inject the distributed-tracing headers into the outgoing request before the
// call and return the caller's error unchanged.
func TestDoClient_InjectsTracingHeaders(t *testing.T) {
	startAgent(t)

	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/caller")
	defer tracer.EndSpan()

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	req.SetRequestURI("http://localhost:9090/hello")
	req.Header.SetMethod(http.MethodGet)

	res := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(res)
	res.SetStatusCode(http.StatusTeapot)

	want := errors.New("dial failed")
	err := DoClient(func() error { return want }, pinpoint.NewContext(context.Background(), tracer), req, res)

	if !errors.Is(err, want) {
		t.Errorf("DoClient() = %v, want %v", err, want)
	}
	for _, key := range []string{
		pinpoint.HeaderTraceId,
		pinpoint.HeaderSpanId,
		pinpoint.HeaderParentSpanId,
		pinpoint.HeaderParentApplicationName,
		pinpoint.HeaderHost,
	} {
		if len(req.Header.Peek(key)) == 0 {
			t.Errorf("outgoing request is missing the %s header", key)
		}
	}
}

// A context that never had a span yields a noop tracer. DoClient must record
// nothing and still make the call.
func TestDoClient_WithNoopTracer(t *testing.T) {
	startAgent(t)

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	req.SetRequestURI("http://localhost:9090/hello")

	called := false
	if err := DoClient(func() error { called = true; return nil }, context.Background(), req, nil); err != nil {
		t.Fatalf("DoClient() = %v", err)
	}
	if !called {
		t.Error("the request was not made")
	}
}
