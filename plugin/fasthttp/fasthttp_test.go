package ppfasthttp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"testing"

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

func newRequestCtx(method, uri string) *fasthttp.RequestCtx {
	var req fasthttp.Request
	req.Header.SetMethod(method)
	req.SetRequestURI(uri)

	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&req, &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 12345}, nil)
	return ctx
}

// tracerOf reads the tracer back out of the user value the wrapper stores.
func tracerOf(t *testing.T, ctx *fasthttp.RequestCtx) pinpoint.Tracer {
	t.Helper()
	value, ok := ctx.UserValue(CtxKey).(context.Context)
	require.True(t, ok, "the wrapper did not store a context under %q", CtxKey)
	return pinpoint.FromContext(value)
}

// pinpointHeaders are the distributed tracing headers Inject writes; the
// callee continues the transaction from them.
var pinpointHeaders = []string{
	pinpoint.HeaderTraceId,
	pinpoint.HeaderSpanId,
	pinpoint.HeaderParentSpanId,
	pinpoint.HeaderParentApplicationName,
	pinpoint.HeaderHost,
}

// fasthttp stores headers as bytes in its own multi-map. These adapters are
// what the agent reads inbound headers through, so a mistake here silently
// drops every recorded header rather than failing loudly.
func Test_reqHeader(t *testing.T) {
	ctx := newRequestCtx(http.MethodGet, "http://localhost/hello")
	ctx.Request.Header.Set("X-Trace", "abc")
	ctx.Request.Header.Add("X-Multi", "one")
	ctx.Request.Header.Add("X-Multi", "two")
	h := RequestHeader{&ctx.Request.Header}

	assert.Equal(t, "abc", h.Get("X-Trace"))
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
}

func Test_resHeader(t *testing.T) {
	ctx := newRequestCtx(http.MethodGet, "http://localhost/hello")
	ctx.Response.Header.Set("X-Result", "ok")
	h := ResponseHeader{&ctx.Response.Header}

	assert.Equal(t, []string{"ok"}, h.Values("X-Result"))
	assert.Equal(t, []string{""}, h.Values("X-Absent"), "an absent response header reads as one empty value")

	visited := map[string][]string{}
	h.VisitAll(func(name string, values []string) { visited[name] = values })
	assert.Equal(t, []string{"ok"}, visited["X-Result"])
}

// Cookies live in the Cookie request header; the adapter has to split them
// into pairs so the cookie recorder sees names, not one blob.
func Test_cookie(t *testing.T) {
	ctx := newRequestCtx(http.MethodGet, "http://localhost/hello")
	ctx.Request.Header.SetCookie("first", "1")
	ctx.Request.Header.SetCookie("second", "2")

	got := map[string]string{}
	Cookie{&ctx.Request.Header}.VisitAll(func(name, value string) { got[name] = value })

	assert.Equal(t, map[string]string{"first": "1", "second": "2"}, got)
}

// A request without cookies must simply yield nothing.
func Test_cookie_Empty(t *testing.T) {
	ctx := newRequestCtx(http.MethodGet, "http://localhost/hello")

	Cookie{&ctx.Request.Header}.VisitAll(func(string, string) {
		t.Error("a request without cookies yielded one")
	})
}

// The handler reads its tracer out of the user value the wrapper stores, so
// that value must be a context carrying a sampled tracer.
func TestWrapHandler_PutsSampledTracerInUserValue(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	h := WrapHandler(func(ctx *fasthttp.RequestCtx) {
		tracer = tracerOf(t, ctx)
		ctx.SetStatusCode(http.StatusTeapot)
		ctx.SetBodyString("hello")
	}, "/hello/{name}")

	ctx := newRequestCtx(http.MethodGet, "http://localhost/hello/pinpoint")
	h(ctx)

	require.NotNil(t, tracer, "no tracer in the handler's user value")
	assert.True(t, tracer.IsSampled(), "handler received an unsampled tracer")
	assert.Equal(t, http.StatusTeapot, ctx.Response.StatusCode())
	assert.Equal(t, "hello", string(ctx.Response.Body()))
}

// The wrapper never converts the fasthttp request to a net/http one, so the
// span attributes have to be read straight off the fasthttp context.
func TestWrapHandler_RecordsRequestAttributesOnTheSpan(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	h := WrapHandler(func(ctx *fasthttp.RequestCtx) { tracer = tracerOf(t, ctx) }, "/hello/{name}")

	ctx := newRequestCtx(http.MethodGet, "http://myhost:8080/hello/pinpoint")
	h(ctx)

	span := spanOf(t, tracer)
	assert.Equal(t, "/hello/pinpoint", span["RpcName"], "the span is named after the request path, not the route pattern")
	assert.Equal(t, "myhost:8080", span["EndPoint"])
	assert.Equal(t, "10.0.0.1", span["RemoteAddr"], "the peer address must be stripped of its port")
}

// X-Forwarded-For overrides the transport peer address, exactly as it does for
// net/http; the fasthttp header adapter is what makes that reachable.
func TestWrapHandler_ResolvesTheForwardedRemoteAddress(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	h := WrapHandler(func(ctx *fasthttp.RequestCtx) { tracer = tracerOf(t, ctx) })

	ctx := newRequestCtx(http.MethodGet, "http://localhost/hello")
	ctx.Request.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.2")
	h(ctx)

	assert.Equal(t, "203.0.113.7", spanOf(t, tracer)["RemoteAddr"])
}

// A fasthttp service is usually one hop of a larger call: the tracing headers
// the caller sent have to put this span in the caller's transaction.
func TestWrapHandler_ContinuesTheCallersTransaction(t *testing.T) {
	startAgent(t)

	caller := pinpoint.GetAgent().NewSpanTracer("caller", "/caller")
	defer caller.EndSpan()

	ctx := newRequestCtx(http.MethodGet, "http://localhost/hello")
	caller.NewSpanEvent("call")
	caller.Inject(&distributedTracingContextWriterMD{&ctx.Request.Header})
	caller.EndSpanEvent()

	var tracer pinpoint.Tracer
	WrapHandler(func(ctx *fasthttp.RequestCtx) { tracer = tracerOf(t, ctx) })(ctx)

	require.NotNil(t, tracer)
	assert.Equal(t, caller.TransactionId().String(), tracer.TransactionId().String())
}

// The status the span records is fasthttp's response status, read after the
// handler has run.
func TestWrapHandler_RecordsTheFinalStatus(t *testing.T) {
	tests := []struct {
		name     string
		handler  fasthttp.RequestHandler
		wantCode int
		wantFail bool
	}{
		{
			name:     "an explicit status",
			handler:  func(ctx *fasthttp.RequestCtx) { ctx.SetStatusCode(http.StatusTeapot) },
			wantCode: http.StatusTeapot,
		},
		{
			name:     "a body without a status leaves fasthttp's implicit 200",
			handler:  func(ctx *fasthttp.RequestCtx) { ctx.SetBodyString("hi") },
			wantCode: http.StatusOK,
		},
		{
			name:     "a configured error status fails the span",
			handler:  func(ctx *fasthttp.RequestCtx) { ctx.SetStatusCode(http.StatusServiceUnavailable) },
			wantCode: http.StatusServiceUnavailable,
			wantFail: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			startAgent(t)

			var tracer pinpoint.Tracer
			ctx := newRequestCtx(http.MethodGet, "http://localhost/hello")
			WrapHandler(func(c *fasthttp.RequestCtx) {
				tracer = tracerOf(t, c)
				tt.handler(c)
			})(ctx)

			assert.Equal(t, tt.wantCode, ctx.Response.StatusCode())
			assert.Equal(t, tt.wantFail, spanOf(t, tracer)["Err"] != float64(0),
				"the default 5xx error class decides whether the span fails")
		})
	}
}

// The route pattern is optional; without it the wrapper skips URL statistics
// and still traces the call.
func TestWrapHandler_WithoutARoutePattern(t *testing.T) {
	startAgent(t, pinpoint.WithHttpUrlStatEnable(true))

	var tracer pinpoint.Tracer
	assert.NotPanics(t, func() {
		WrapHandler(func(ctx *fasthttp.RequestCtx) {
			tracer = tracerOf(t, ctx)
		})(newRequestCtx(http.MethodGet, "http://localhost/hello"))
	})

	require.NotNil(t, tracer, "the handler did not run")
	assert.True(t, tracer.IsSampled())
}

// The wrapper marks the span failed and re-panics; swallowing the panic would
// turn a crash fasthttp's server reports into a silent 200.
func TestWrapHandler_PanicPropagates(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	assert.PanicsWithValue(t, "boom", func() {
		WrapHandler(func(ctx *fasthttp.RequestCtx) {
			tracer = tracerOf(t, ctx)
			panic("boom")
		})(newRequestCtx(http.MethodGet, "http://localhost/boom"))
	}, "the wrapper swallowed the handler panic")

	assert.NotEqual(t, float64(0), spanOf(t, tracer)["Err"], "a panicking handler must fail the span")
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
		assert.Nil(t, ctx.UserValue(CtxKey), "a disabled agent still stored a tracer context")
		ctx.SetStatusCode(http.StatusNoContent)
	})

	ctx := newRequestCtx(http.MethodGet, "http://localhost/hello")
	h(ctx)

	require.True(t, called, "the handler did not run")
	assert.Equal(t, http.StatusNoContent, ctx.Response.StatusCode())
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

	assert.ErrorIs(t, err, want, "the caller's error must come back unchanged")
	for _, key := range pinpointHeaders {
		assert.NotEmpty(t, req.Header.Peek(key), "outgoing request is missing the %s header", key)
	}
	assert.Equal(t, tracer.TransactionId().String(), string(req.Header.Peek(pinpoint.HeaderTraceId)))
}

// The callee reads the headers back through the same adapter the server side
// uses, and has to land in the caller's transaction.
func TestDoClient_AndWrapHandlerShareOneTransaction(t *testing.T) {
	startAgent(t)

	caller := pinpoint.GetAgent().NewSpanTracer("caller", "/caller")
	defer caller.EndSpan()

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	req.SetRequestURI("http://localhost:9090/callee")
	req.Header.SetMethod(http.MethodGet)

	require.NoError(t, DoClient(func() error { return nil },
		pinpoint.NewContext(context.Background(), caller), req, nil))

	// Hand the injected headers to the server side.
	var calleeReq fasthttp.Request
	req.CopyTo(&calleeReq)
	calleeCtx := &fasthttp.RequestCtx{}
	calleeCtx.Init(&calleeReq, &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 1}, nil)

	var callee pinpoint.Tracer
	WrapHandler(func(c *fasthttp.RequestCtx) { callee = tracerOf(t, c) })(calleeCtx)

	require.NotNil(t, callee)
	assert.Equal(t, caller.TransactionId().String(), callee.TransactionId().String())
}

// A context that never had a span yields a noop tracer. DoClient must record
// nothing and still make the call.
func TestDoClient_WithNoopTracer(t *testing.T) {
	startAgent(t)

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	req.SetRequestURI("http://localhost:9090/hello")

	called := false
	err := DoClient(func() error { called = true; return nil }, context.Background(), req, nil)

	require.NoError(t, err)
	assert.True(t, called, "the request was not made")
}

// A nil response is what a caller passes when it does not need one back; the
// status annotation is simply skipped.
func TestDoClient_WithoutAResponse(t *testing.T) {
	startAgent(t)

	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/caller")
	defer tracer.EndSpan()

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	req.SetRequestURI("http://localhost:9090/hello")

	assert.NotPanics(t, func() {
		_ = DoClient(func() error { return nil }, pinpoint.NewContext(context.Background(), tracer), req, nil)
	})
}

type endCountingTracer struct {
	pinpoint.Tracer
	ends int
}

func (t *endCountingTracer) NewSpanEvent(string) pinpoint.Tracer { return t }
func (t *endCountingTracer) EndSpanEvent()                       { t.ends++ }

// A panicking doFunc must still close the span event on its way up.
func TestDoClient_PanicStillClosesTheSpanEvent(t *testing.T) {
	startAgent(t)
	tracer := &endCountingTracer{Tracer: pinpoint.NoopTracer()}
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)

	assert.PanicsWithValue(t, "boom", func() {
		_ = DoClient(func() error { panic("boom") }, pinpoint.NewContext(context.Background(), tracer), req, nil)
	})
	assert.Equal(t, 1, tracer.ends, "the span event must be closed during panic unwinding")
}
