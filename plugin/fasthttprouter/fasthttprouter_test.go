package ppfasthttprouter

import (
	"context"
	"net"
	"net/http"
	"testing"

	ppfasthttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp/v2"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func serve(handler fasthttp.RequestHandler, method, uri string) *fasthttp.RequestCtx {
	var req fasthttp.Request
	req.Header.SetMethod(method)
	req.SetRequestURI(uri)

	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&req, &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 12345}, nil)
	handler(ctx)
	return ctx
}

// tracerOf reads the tracer back out of the user value the fasthttp wrapper
// stores under its own key.
func tracerOf(t *testing.T, ctx *fasthttp.RequestCtx) pinpoint.Tracer {
	t.Helper()
	value, ok := ctx.UserValue(ppfasthttp.CtxKey).(context.Context)
	require.True(t, ok, "the wrapper did not store a context under %q", ppfasthttp.CtxKey)
	return pinpoint.FromContext(value)
}

// Every method helper, ANY and Handle have to register the wrapped handler
// under the same path and method, or the route silently disappears. Each one is registered on its
// own router and driven end to end, checking that the route parameter still
// resolves and the tracer reaches the handler.
func TestRouter_AllMethodsStayRouted(t *testing.T) {
	pptest.StartAgent(t)

	for _, tt := range []struct {
		method   string
		register func(*Router, string, fasthttp.RequestHandler)
	}{
		{http.MethodGet, (*Router).GET},
		{http.MethodHead, (*Router).HEAD},
		{http.MethodPost, (*Router).POST},
		{http.MethodPut, (*Router).PUT},
		{http.MethodPatch, (*Router).PATCH},
		{http.MethodDelete, (*Router).DELETE},
		{http.MethodConnect, (*Router).CONNECT},
		{http.MethodOptions, (*Router).OPTIONS},
		{http.MethodTrace, (*Router).TRACE},
		// ANY registers one handler for every method, a custom one included.
		{"PROPFIND", (*Router).ANY},
		// Handle takes the method as an argument rather than from the helper name.
		{"PURGE", func(r *Router, p string, h fasthttp.RequestHandler) { r.Handle("PURGE", p, h) }},
	} {
		t.Run(tt.method, func(t *testing.T) {
			r := New()
			var name string
			var tracer pinpoint.Tracer
			tt.register(r, "/hello/{name}", func(ctx *fasthttp.RequestCtx) {
				name, _ = ctx.UserValue("name").(string)
				tracer = tracerOf(t, ctx)
				ctx.SetStatusCode(http.StatusNoContent)
			})

			ctx := serve(r.Handler, tt.method, "http://localhost/hello/pinpoint")

			assert.Equal(t, "pinpoint", name, "route parameter")
			require.NotNil(t, tracer, "the %s wrapper did not trace", tt.method)
			assert.True(t, tracer.IsSampled(), "handler received an unsampled tracer")
			assert.Equal(t, http.StatusNoContent, ctx.Response.StatusCode())
		})
	}
}

// The router knows the route pattern, so it hands it to the wrapper for URL
// statistics while the span still names itself after the concrete path.
func TestRouter_SpanNameIsTheRequestPath(t *testing.T) {
	pptest.StartAgent(t, pinpoint.WithHttpUrlStatEnable(true))

	r := New()
	var tracer pinpoint.Tracer
	r.GET("/hello/{name}", func(ctx *fasthttp.RequestCtx) { tracer = tracerOf(t, ctx) })

	serve(r.Handler, http.MethodGet, "http://myhost:8080/hello/pinpoint")

	span := pptest.SpanOf(t, tracer)
	assert.Equal(t, "/hello/pinpoint", span["RpcName"])
	assert.Equal(t, "myhost:8080", span["EndPoint"])
	assert.Equal(t, "10.0.0.1", span["RemoteAddr"])
}

// Registering a route must not change what the router does with the methods it
// was not registered for.
func TestRouter_UnregisteredMethodStillRejected(t *testing.T) {
	pptest.StartAgent(t)

	r := New()
	r.GET("/hello", func(ctx *fasthttp.RequestCtx) { t.Error("the GET handler ran for a POST") })

	ctx := serve(r.Handler, http.MethodPost, "http://localhost/hello")

	assert.Equal(t, http.StatusMethodNotAllowed, ctx.Response.StatusCode())
}

// A path no route matches is the router's own 404 and never reaches an
// instrumented handler.
func TestRouter_UnmatchedPath(t *testing.T) {
	pptest.StartAgent(t)

	r := New()
	r.GET("/hello", func(ctx *fasthttp.RequestCtx) { t.Error("the handler ran for an unmatched path") })

	ctx := serve(r.Handler, http.MethodGet, "http://localhost/nowhere")

	assert.Equal(t, http.StatusNotFound, ctx.Response.StatusCode())
}

// A route registered through the router re-panics exactly as the bare fasthttp
// wrapper does.
func TestRouter_PanicPropagates(t *testing.T) {
	pptest.StartAgent(t)

	r := New()
	var tracer pinpoint.Tracer
	r.GET("/boom", func(ctx *fasthttp.RequestCtx) {
		tracer = tracerOf(t, ctx)
		panic("boom")
	})

	assert.PanicsWithValue(t, "boom", func() {
		serve(r.Handler, http.MethodGet, "http://localhost/boom")
	}, "the wrapper swallowed the handler panic")

	assert.NotEqual(t, float64(0), pptest.SpanOf(t, tracer)["Err"], "a panicking handler must fail the span")
}

// With no agent running the router must be a straight pass-through.
func TestRouter_PassesThroughWhenAgentDisabled(t *testing.T) {
	r := New()
	called := false
	r.GET("/hello/{name}", func(ctx *fasthttp.RequestCtx) {
		called = true
		assert.Equal(t, "pinpoint", ctx.UserValue("name"), "route parameter")
		assert.Nil(t, ctx.UserValue(ppfasthttp.CtxKey), "a disabled agent still stored a tracer context")
		ctx.SetStatusCode(http.StatusNoContent)
	})

	ctx := serve(r.Handler, http.MethodGet, "http://localhost/hello/pinpoint")

	require.True(t, called, "the handler did not run")
	assert.Equal(t, http.StatusNoContent, ctx.Response.StatusCode())
}
