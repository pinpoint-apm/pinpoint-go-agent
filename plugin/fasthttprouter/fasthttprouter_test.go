package ppfasthttprouter

import (
	"context"
	"net"
	"net/http"
	"testing"

	ppfasthttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp"

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

func serve(handler fasthttp.RequestHandler, method, uri string) *fasthttp.RequestCtx {
	var req fasthttp.Request
	req.Header.SetMethod(method)
	req.SetRequestURI(uri)

	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&req, &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 12345}, nil)
	handler(ctx)
	return ctx
}

// Every method helper has to register the wrapped handler under the same path
// and method, or the route silently disappears. Each one is registered on its
// own router and driven end to end, checking that the route parameter still
// resolves and the tracer reaches the handler.
func TestRouter_AllMethodsStayRouted(t *testing.T) {
	startAgent(t)

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
	} {
		t.Run(tt.method, func(t *testing.T) {
			r := New()
			var name string
			var sampled bool
			tt.register(r, "/hello/{name}", func(ctx *fasthttp.RequestCtx) {
				name, _ = ctx.UserValue("name").(string)
				sampled = pinpoint.FromContext(ctx.UserValue(ppfasthttp.CtxKey).(context.Context)).IsSampled()
				ctx.SetStatusCode(http.StatusNoContent)
			})

			ctx := serve(r.Handler, tt.method, "http://localhost/hello/pinpoint")

			if name != "pinpoint" {
				t.Errorf("route parameter = %q, want %q", name, "pinpoint")
			}
			if !sampled {
				t.Error("handler received an unsampled tracer")
			}
			if ctx.Response.StatusCode() != http.StatusNoContent {
				t.Errorf("status = %d, want %d", ctx.Response.StatusCode(), http.StatusNoContent)
			}
		})
	}
}

// ANY registers one handler for every method fasthttp/router knows.
func TestRouter_ANY(t *testing.T) {
	startAgent(t)

	r := New()
	calls := 0
	r.ANY("/hello", func(ctx *fasthttp.RequestCtx) { calls++ })

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		serve(r.Handler, method, "http://localhost/hello")
	}

	if calls != 3 {
		t.Errorf("the handler ran %d times, want 3", calls)
	}
}

// Handle takes the method as an argument rather than from the helper name.
func TestRouter_Handle(t *testing.T) {
	startAgent(t)

	r := New()
	called := false
	r.Handle(http.MethodGet, "/hello/{name}", func(ctx *fasthttp.RequestCtx) { called = true })

	serve(r.Handler, http.MethodGet, "http://localhost/hello/pinpoint")

	if !called {
		t.Error("the handler did not run")
	}
}

// Registering a route must not change what the router does with the methods it
// was not registered for.
func TestRouter_UnregisteredMethodStillRejected(t *testing.T) {
	startAgent(t)

	r := New()
	r.GET("/hello", func(ctx *fasthttp.RequestCtx) { t.Error("the GET handler ran for a POST") })

	ctx := serve(r.Handler, http.MethodPost, "http://localhost/hello")

	if ctx.Response.StatusCode() != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", ctx.Response.StatusCode(), http.StatusMethodNotAllowed)
	}
}
