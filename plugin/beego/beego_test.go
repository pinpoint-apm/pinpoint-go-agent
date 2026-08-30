package ppbeego

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/beego/beego/v2/client/httplib"
	beegoContext "github.com/beego/beego/v2/server/web/context"
	"github.com/pinpoint-apm/pinpoint-go-agent"
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

func newBeegoContext(req *http.Request, rec *httptest.ResponseRecorder) *beegoContext.Context {
	ctx := beegoContext.NewContext()
	ctx.Reset(rec, req)
	return ctx
}

// The filter runs in front of every handler, so it must leave beego's own
// behaviour intact and hand the handler the tracer-carrying request.
func TestServerFilterChain_TracesAndPassesTheContextThrough(t *testing.T) {
	startAgent(t)

	rec := httptest.NewRecorder()
	ctx := newBeegoContext(httptest.NewRequest(http.MethodGet, "/hello", nil), rec)
	ctx.Input.SetData("RouterPattern", "/hello/:name")

	var tracer pinpoint.Tracer
	ServerFilterChain()(func(c *beegoContext.Context) {
		tracer = pinpoint.TracerFromRequestContext(c.Request)
		c.Output.SetStatus(http.StatusTeapot)
		c.ResponseWriter.WriteHeader(http.StatusTeapot)
	})(ctx)

	if tracer == nil {
		t.Fatal("no tracer in the handler's request context")
	}
	if !tracer.IsSampled() {
		t.Error("handler received an unsampled tracer")
	}
	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
}

// Input.GetData is an interface{} store keyed by string that the application
// shares with beego. Anything it holds under "RouterPattern" reaches the
// deferred URL-stat collection, and a non-string value must not take the
// request down with it.
func TestServerFilterChain_ForeignRouterPatternValue(t *testing.T) {
	startAgent(t)

	for _, value := range []interface{}{nil, 42, struct{ Path string }{"/hello"}} {
		rec := httptest.NewRecorder()
		ctx := newBeegoContext(httptest.NewRequest(http.MethodGet, "/hello", nil), rec)
		if value != nil {
			ctx.Input.SetData("RouterPattern", value)
		}

		called := false
		ServerFilterChain()(func(c *beegoContext.Context) { called = true })(ctx)

		if !called {
			t.Errorf("RouterPattern=%v: the handler did not run", value)
		}
	}
}

// The wrapper marks the span failed and re-panics; swallowing the panic would
// turn a crash beego's recover filter reports into a silent 200.
func TestServerFilterChain_PanicPropagates(t *testing.T) {
	startAgent(t)

	ctx := newBeegoContext(httptest.NewRequest(http.MethodGet, "/boom", nil), httptest.NewRecorder())

	defer func() {
		if recover() == nil {
			t.Error("the wrapper swallowed the handler panic")
		}
	}()
	ServerFilterChain()(func(*beegoContext.Context) { panic("boom") })(ctx)
}

// With no agent running the filter must be a straight pass-through.
func TestServerFilterChain_PassesThroughWhenAgentDisabled(t *testing.T) {
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

	ctx := newBeegoContext(httptest.NewRequest(http.MethodGet, "/hello", nil), httptest.NewRecorder())

	called := false
	ServerFilterChain()(func(c *beegoContext.Context) {
		called = true
		if pinpoint.TracerFromRequestContext(c.Request).IsSampled() {
			t.Error("a disabled agent produced a sampled tracer")
		}
	})(ctx)

	if !called {
		t.Fatal("the handler did not run")
	}
}

// Middleware is the deprecated net/http form of the server filter. It still
// has to trace the handler and leave the response untouched.
func TestMiddleware_TracesAndPreservesTheResponse(t *testing.T) {
	startAgent(t)

	var sampled bool
	h := Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sampled = pinpoint.TracerFromRequestContext(r).IsSampled()
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hello"))
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello", nil))

	if !sampled {
		t.Error("handler received an unsampled tracer")
	}
	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
	if rec.Body.String() != "hello" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "hello")
	}
}

// The client filter is what links the caller's span to the callee's, so it has
// to inject the distributed-tracing headers into the outgoing request before
// the next filter sends it, and return that filter's result unchanged.
func TestClientFilterChain_InjectsTracingHeaders(t *testing.T) {
	startAgent(t)

	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/caller")
	defer tracer.EndSpan()

	req := httplib.Get("http://localhost:9090/hello")
	want := &http.Response{StatusCode: http.StatusTeapot}

	var sentHeader http.Header
	resp, err := ClientFilterChain(tracer)(func(ctx context.Context, r *httplib.BeegoHTTPRequest) (*http.Response, error) {
		sentHeader = r.GetRequest().Header.Clone()
		return want, nil
	})(context.Background(), req)

	if err != nil {
		t.Fatalf("filter returned %v", err)
	}
	if resp != want {
		t.Errorf("filter returned %v, want the next filter's response", resp)
	}
	for _, key := range []string{
		pinpoint.HeaderTraceId,
		pinpoint.HeaderSpanId,
		pinpoint.HeaderParentSpanId,
		pinpoint.HeaderParentApplicationName,
		pinpoint.HeaderHost,
	} {
		if sentHeader.Get(key) == "" {
			t.Errorf("outgoing request is missing the %s header", key)
		}
	}
}

// A transport failure has to reach the caller unchanged; the filter only
// records it.
func TestClientFilterChain_ReturnsTheTransportError(t *testing.T) {
	startAgent(t)

	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/caller")
	defer tracer.EndSpan()

	want := errors.New("dial failed")
	resp, err := ClientFilterChain(tracer)(func(context.Context, *httplib.BeegoHTTPRequest) (*http.Response, error) {
		return nil, want
	})(context.Background(), httplib.Get("http://localhost:9090/hello"))

	if !errors.Is(err, want) {
		t.Errorf("filter returned %v, want %v", err, want)
	}
	if resp != nil {
		t.Errorf("filter returned a response along with an error: %v", resp)
	}
}

// The client filter is handed the tracer explicitly, and application code can
// pass one from a context that never had a span - a noop tracer. That must
// record nothing and still send the request.
func TestClientFilterChain_WithNoopTracer(t *testing.T) {
	startAgent(t)

	called := false
	_, err := ClientFilterChain(pinpoint.FromContext(context.Background()))(
		func(context.Context, *httplib.BeegoHTTPRequest) (*http.Response, error) {
			called = true
			return &http.Response{StatusCode: http.StatusOK}, nil
		})(context.Background(), httplib.Get("http://localhost:9090/hello"))

	if err != nil {
		t.Fatalf("filter returned %v", err)
	}
	if !called {
		t.Error("the next filter did not run")
	}
}
