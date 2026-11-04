package ppfasthttprouter

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

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

// A group's routes, nested groups included, are traced like the router's own:
// fasthttp/router's Group registers on the router underneath, past the
// wrapping, so they used to run untraced and without the CtxKey value.
func TestRouter_GroupRoutesAreTraced(t *testing.T) {
	pptest.StartAgent(t)

	r := New()
	api := r.Group("/api")
	v1 := api.Group("/v1")
	same := v1.Group("/") // fasthttp/router returns the group itself

	for _, tt := range []struct {
		name     string
		register func(path string, h fasthttp.RequestHandler)
		uri      string
	}{
		{"group", func(p string, h fasthttp.RequestHandler) { api.GET(p, h) }, "/api/users/pinpoint"},
		{"nested group", func(p string, h fasthttp.RequestHandler) { v1.POST(p, h) }, "/api/v1/users/pinpoint"},
		{"root of a group", func(p string, h fasthttp.RequestHandler) { same.Handle("PURGE", p, h) }, "/api/v1/users/pinpoint"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var name string
			var tracer pinpoint.Tracer
			tt.register("/users/{name}", func(ctx *fasthttp.RequestCtx) {
				name, _ = ctx.UserValue("name").(string)
				tracer = tracerOf(t, ctx)
			})

			method := map[string]string{"group": http.MethodGet, "nested group": http.MethodPost, "root of a group": "PURGE"}[tt.name]
			serve(r.Handler, method, "http://localhost"+tt.uri)

			assert.Equal(t, "pinpoint", name, "route parameter")
			require.NotNil(t, tracer, "the group route was not traced")
			assert.True(t, tracer.IsSampled())
			assert.Equal(t, tt.uri, pptest.SpanOf(t, tracer)["RpcName"])
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

// A nil handler is refused at registration, as fasthttp/router refuses it
// unwrapped, rather than registered and called on the first request.
func TestRouter_NilHandlerPanicsAtRegistration(t *testing.T) {
	r := New()
	assert.PanicsWithValue(t, "handler must not be nil", func() { r.GET("/x", nil) })
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
		tracer := pinpoint.FromContext(ctx.UserValue(ppfasthttp.CtxKey).(context.Context))
		assert.Equal(t, pinpoint.NoopTracer(), tracer, "a disabled agent must hand the handler a no-op tracer")
		ctx.SetStatusCode(http.StatusNoContent)
	})

	ctx := serve(r.Handler, http.MethodGet, "http://localhost/hello/pinpoint")

	require.True(t, called, "the handler did not run")
	assert.Equal(t, http.StatusNoContent, ctx.Response.StatusCode())
}

// A group's method helpers register on the router underneath with the group
// prefix, so each has to keep its route and method and still trace, like the
// router's own.
func TestGroup_AllMethodsStayRouted(t *testing.T) {
	pptest.StartAgent(t)

	for _, tt := range []struct {
		method   string
		register func(*Group, string, fasthttp.RequestHandler)
	}{
		{http.MethodGet, (*Group).GET},
		{http.MethodHead, (*Group).HEAD},
		{http.MethodPost, (*Group).POST},
		{http.MethodPut, (*Group).PUT},
		{http.MethodPatch, (*Group).PATCH},
		{http.MethodDelete, (*Group).DELETE},
		{http.MethodConnect, (*Group).CONNECT},
		{http.MethodOptions, (*Group).OPTIONS},
		{http.MethodTrace, (*Group).TRACE},
		{"PROPFIND", (*Group).ANY},
		{"PURGE", func(g *Group, p string, h fasthttp.RequestHandler) { g.Handle("PURGE", p, h) }},
	} {
		t.Run(tt.method, func(t *testing.T) {
			r := New()
			var name string
			var tracer pinpoint.Tracer
			tt.register(r.Group("/api"), "/hello/{name}", func(ctx *fasthttp.RequestCtx) {
				name, _ = ctx.UserValue("name").(string)
				tracer = tracerOf(t, ctx)
				ctx.SetStatusCode(http.StatusNoContent)
			})

			ctx := serve(r.Handler, tt.method, "http://localhost/api/hello/pinpoint")

			assert.Equal(t, "pinpoint", name, "route parameter")
			require.NotNil(t, tracer, "the group %s wrapper did not trace", tt.method)
			assert.True(t, tracer.IsSampled(), "handler received an unsampled tracer")
			assert.Equal(t, http.StatusNoContent, ctx.Response.StatusCode())
			assert.Equal(t, "/api/hello/pinpoint", pptest.SpanOf(t, tracer)["RpcName"])
		})
	}
}

// A group's file routes are served under the group prefix, untraced like the
// router's.
func TestGroup_ServeFilesUntraced(t *testing.T) {
	pptest.StartAgent(t)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("from disk"), 0o600))

	for _, tt := range []struct {
		name     string
		register func(*Group)
		want     string
	}{
		{"ServeFiles", func(g *Group) { g.ServeFiles("/static/{filepath:*}", dir) }, "from disk"},
		{"ServeFS", func(g *Group) {
			g.ServeFS("/static/{filepath:*}", fstest.MapFS{"hello.txt": {Data: []byte("from fs")}})
		}, "from fs"},
		{"ServeFilesCustom", func(g *Group) {
			g.ServeFilesCustom("/static/{filepath:*}", &fasthttp.FS{Root: dir})
		}, "from disk"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := New()
			tt.register(r.Group("/assets"))

			ctx := serve(r.Handler, http.MethodGet, "http://localhost/assets/static/hello.txt")

			assert.Equal(t, http.StatusOK, ctx.Response.StatusCode())
			assert.Equal(t, tt.want, string(ctx.Response.Body()))
			assert.Nil(t, ctx.UserValue(ppfasthttp.CtxKey), "a file route was traced")
		})
	}
}
