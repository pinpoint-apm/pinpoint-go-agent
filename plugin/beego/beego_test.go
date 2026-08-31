package ppbeego

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/beego/beego/v2/client/httplib"
	beegoContext "github.com/beego/beego/v2/server/web/context"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func newBeegoContext(req *http.Request, rec *httptest.ResponseRecorder) *beegoContext.Context {
	ctx := beegoContext.NewContext()
	ctx.Reset(rec, req)
	return ctx
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

// The filter runs in front of every handler, so it must leave beego's own
// behaviour intact and hand the handler the tracer-carrying request.
func TestServerFilterChain_TracesAndPassesTheContextThrough(t *testing.T) {
	startAgent(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	req.Host = "myhost:8080"
	req.RemoteAddr = "10.0.0.1:4242"
	ctx := newBeegoContext(req, rec)
	ctx.Input.SetData("RouterPattern", "/hello/:name")

	var tracer pinpoint.Tracer
	ServerFilterChain()(func(c *beegoContext.Context) {
		tracer = pinpoint.TracerFromRequestContext(c.Request)
		c.Output.SetStatus(http.StatusTeapot)
		c.ResponseWriter.WriteHeader(http.StatusTeapot)
	})(ctx)

	require.NotNil(t, tracer, "no tracer in the handler's request context")
	assert.True(t, tracer.IsSampled(), "handler received an unsampled tracer")
	assert.Equal(t, http.StatusTeapot, rec.Code)

	span := spanOf(t, tracer)
	assert.Equal(t, "/hello", span["RpcName"], "the span is named after the request path, not the router pattern")
	assert.Equal(t, "myhost:8080", span["EndPoint"])
	assert.Equal(t, "10.0.0.1", span["RemoteAddr"])
}

// The status the span records is beego's Output.Status, set after the handler
// has run; a configured error class turns the span red.
func TestServerFilterChain_RecordsTheFinalStatus(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		wantFail bool
	}{
		{name: "a success status", status: http.StatusOK},
		{name: "a client error is not a failure by default", status: http.StatusNotFound},
		{name: "a server error fails the span", status: http.StatusInternalServerError, wantFail: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			startAgent(t)

			ctx := newBeegoContext(httptest.NewRequest(http.MethodGet, "/hello", nil), httptest.NewRecorder())
			var tracer pinpoint.Tracer
			ServerFilterChain()(func(c *beegoContext.Context) {
				tracer = pinpoint.TracerFromRequestContext(c.Request)
				c.Output.SetStatus(tt.status)
			})(ctx)

			assert.Equal(t, tt.wantFail, spanOf(t, tracer)["Err"] != float64(0),
				"the default 5xx error class decides whether the span fails")
		})
	}
}

// A beego service is usually one hop of a larger call: the tracing headers the
// caller sent have to put this span in the caller's transaction.
func TestServerFilterChain_ContinuesTheCallersTransaction(t *testing.T) {
	startAgent(t)

	caller := pinpoint.GetAgent().NewSpanTracer("caller", "/caller")
	defer caller.EndSpan()
	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	caller.NewSpanEvent("call")
	caller.Inject(req.Header)
	caller.EndSpanEvent()

	ctx := newBeegoContext(req, httptest.NewRecorder())
	var tracer pinpoint.Tracer
	ServerFilterChain()(func(c *beegoContext.Context) {
		tracer = pinpoint.TracerFromRequestContext(c.Request)
	})(ctx)

	require.NotNil(t, tracer)
	assert.Equal(t, caller.TransactionId().String(), tracer.TransactionId().String())
}

// Input.GetData is an interface{} store keyed by string that the application
// shares with beego. Anything it holds under "RouterPattern" reaches the
// deferred URL-stat collection, and a non-string value must not take the
// request down with it.
func TestServerFilterChain_ForeignRouterPatternValue(t *testing.T) {
	startAgent(t, pinpoint.WithHttpUrlStatEnable(true))

	for _, value := range []interface{}{nil, 42, struct{ Path string }{"/hello"}, []string{"/hello"}} {
		rec := httptest.NewRecorder()
		ctx := newBeegoContext(httptest.NewRequest(http.MethodGet, "/hello", nil), rec)
		if value != nil {
			ctx.Input.SetData("RouterPattern", value)
		}

		called := false
		assert.NotPanics(t, func() {
			ServerFilterChain()(func(c *beegoContext.Context) { called = true })(ctx)
		}, "RouterPattern=%v took the request down", value)
		assert.True(t, called, "RouterPattern=%v: the handler did not run", value)
	}
}

// The wrapper marks the span failed and re-panics; swallowing the panic would
// turn a crash beego's recover filter reports into a silent 200.
func TestServerFilterChain_PanicPropagates(t *testing.T) {
	startAgent(t)

	ctx := newBeegoContext(httptest.NewRequest(http.MethodGet, "/boom", nil), httptest.NewRecorder())

	var tracer pinpoint.Tracer
	assert.PanicsWithValue(t, "boom", func() {
		ServerFilterChain()(func(c *beegoContext.Context) {
			tracer = pinpoint.TracerFromRequestContext(c.Request)
			panic("boom")
		})(ctx)
	}, "the wrapper swallowed the handler panic")

	assert.NotEqual(t, float64(0), spanOf(t, tracer)["Err"], "a panicking handler must fail the span")
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
		assert.False(t, pinpoint.TracerFromRequestContext(c.Request).IsSampled(),
			"a disabled agent produced a sampled tracer")
	})(ctx)

	require.True(t, called, "the handler did not run")
}

// Middleware is the deprecated net/http form of the server filter. It still
// has to trace the handler and leave the response untouched.
func TestMiddleware_TracesAndPreservesTheResponse(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	h := Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(r)
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hello"))
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello", nil))

	require.NotNil(t, tracer)
	assert.True(t, tracer.IsSampled(), "handler received an unsampled tracer")
	assert.Equal(t, http.StatusTeapot, rec.Code)
	assert.Equal(t, "hello", rec.Body.String())
	assert.Equal(t, "/hello", spanOf(t, tracer)["RpcName"])
}

// The deprecated middleware re-panics too.
func TestMiddleware_PanicPropagates(t *testing.T) {
	startAgent(t)

	h := Middleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))

	assert.PanicsWithValue(t, "boom", func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
	})
}

func TestMiddleware_PassesThroughWhenAgentDisabled(t *testing.T) {
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

	called := false
	h := Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello", nil))

	require.True(t, called, "the handler did not run")
	assert.Equal(t, http.StatusNoContent, rec.Code)
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

	require.NoError(t, err)
	assert.Same(t, want, resp, "the next filter's response must be returned unchanged")
	for _, key := range pinpointHeaders {
		assert.NotEmpty(t, sentHeader.Get(key), "outgoing request is missing the %s header", key)
	}

	// The callee reads those headers back and lands in the same transaction.
	assert.Equal(t, tracer.TransactionId().String(), sentHeader.Get(pinpoint.HeaderTraceId))
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

	assert.ErrorIs(t, err, want)
	assert.Nil(t, resp, "the filter returned a response along with an error")
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

	require.NoError(t, err)
	assert.True(t, called, "the next filter did not run")
}

// DoRequest is the deprecated client wrapper; it has to inject the same
// headers ClientFilterChain does.
func TestDoRequest(t *testing.T) {
	startAgent(t)

	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/caller")
	defer tracer.EndSpan()

	// No server is listening, so the request fails - what matters is that the
	// headers were injected before the attempt and the error came back.
	req := httplib.Get("http://127.0.0.1:1/hello")
	_, err := DoRequest(tracer, req)
	assert.Error(t, err, "an unreachable host must surface its error")

	for _, key := range pinpointHeaders {
		assert.NotEmpty(t, req.GetRequest().Header.Get(key), "outgoing request is missing the %s header", key)
	}
}

// A nil tracer is what callers hand these when tracing is off.
func TestDoRequest_WithNilTracer(t *testing.T) {
	startAgent(t)

	assert.NotPanics(t, func() {
		_, _ = DoRequest(nil, httplib.Get("http://127.0.0.1:1/hello"))
	})
}
