package ppbeego

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/beego/beego/v2/client/httplib"
	beegoContext "github.com/beego/beego/v2/server/web/context"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	pptest.StartAgent(t)

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

	span := pptest.SpanOf(t, tracer)
	assert.Equal(t, "/hello", span["RpcName"], "the span is named after the request path, not the router pattern")
	assert.Equal(t, "myhost:8080", span["EndPoint"])
	assert.Equal(t, "10.0.0.1", span["RemoteAddr"])
}

// The status the span records is beego's Output.Status, set after the handler
// has run; a configured error class turns the span red.
func TestServerFilterChain_RecordsTheFinalStatus(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       bool // write through Output.Body
		wantStatus int
		wantFail   bool
	}{
		{name: "a success status", status: http.StatusOK, wantStatus: http.StatusOK},
		{name: "a client error is not a failure by default", status: http.StatusNotFound, wantStatus: http.StatusNotFound},
		{name: "a server error fails the span", status: http.StatusInternalServerError, wantStatus: http.StatusInternalServerError, wantFail: true},
		// Output.Body - behind ServeJSON, Render and the rest - writes the header
		// and resets Output.Status to 0, and a plain write never sets it, so the
		// status has to be read from the writer beego itself logs.
		{name: "a body without a status is a 200", body: true, wantStatus: http.StatusOK},
		{name: "a success status written through Output.Body", status: http.StatusCreated, body: true, wantStatus: http.StatusCreated},
		{name: "a server error written through Output.Body fails the span", status: http.StatusInternalServerError, body: true, wantStatus: http.StatusInternalServerError, wantFail: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pptest.StartAgent(t)

			rec := httptest.NewRecorder()
			ctx := newBeegoContext(httptest.NewRequest(http.MethodGet, "/hello", nil), rec)
			var tracer pinpoint.Tracer
			ServerFilterChain()(func(c *beegoContext.Context) {
				tracer = pinpoint.TracerFromRequestContext(c.Request)
				if tt.status != 0 {
					c.Output.SetStatus(tt.status)
				}
				if tt.body {
					require.NoError(t, c.Output.Body([]byte("body")))
				}
			})(ctx)

			if tt.body {
				assert.Equal(t, tt.wantStatus, rec.Code, "wire status")
			}
			assert.Equal(t, tt.wantStatus, statusAnnotation(t, tracer), "recorded status")
			assert.Equal(t, tt.wantFail, pptest.SpanOf(t, tracer)["Err"] != float64(0),
				"the default 5xx error class decides whether the span fails")
		})
	}
}

// A beego service is usually one hop of a larger call: the tracing headers the
// caller sent have to put this span in the caller's transaction.
func TestServerFilterChain_ContinuesTheCallersTransaction(t *testing.T) {
	pptest.StartAgent(t)

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
	pptest.StartAgent(t, pinpoint.WithHttpUrlStatEnable(true))

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
	pptest.StartAgent(t)

	ctx := newBeegoContext(httptest.NewRequest(http.MethodGet, "/boom", nil), httptest.NewRecorder())

	var tracer pinpoint.Tracer
	assert.PanicsWithValue(t, "boom", func() {
		ServerFilterChain()(func(c *beegoContext.Context) {
			tracer = pinpoint.TracerFromRequestContext(c.Request)
			panic("boom")
		})(ctx)
	}, "the wrapper swallowed the handler panic")

	assert.NotEqual(t, float64(0), pptest.SpanOf(t, tracer)["Err"], "a panicking handler must fail the span")
}

// With no agent running the filter must be a straight pass-through.
func TestServerFilterChain_PassesThroughWhenAgentDisabled(t *testing.T) {
	ctx := newBeegoContext(httptest.NewRequest(http.MethodGet, "/hello", nil), httptest.NewRecorder())

	called := false
	ServerFilterChain()(func(c *beegoContext.Context) {
		called = true
		assert.False(t, pinpoint.TracerFromRequestContext(c.Request).IsSampled(),
			"a disabled agent produced a sampled tracer")
	})(ctx)

	require.True(t, called, "the handler did not run")
}

// The client filter is what links the caller's span to the callee's, so it has
// to inject the distributed-tracing headers into the outgoing request before
// the next filter sends it, and return that filter's result unchanged.
func TestClientFilterChain_InjectsTracingHeaders(t *testing.T) {
	pptest.StartAgent(t)

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
	pptest.StartAgent(t)

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
	pptest.StartAgent(t)

	called := false
	_, err := ClientFilterChain(pinpoint.FromContext(context.Background()))(
		func(context.Context, *httplib.BeegoHTTPRequest) (*http.Response, error) {
			called = true
			return &http.Response{StatusCode: http.StatusOK}, nil
		})(context.Background(), httplib.Get("http://localhost:9090/hello"))

	require.NoError(t, err)
	assert.True(t, called, "the next filter did not run")
}

// statusAnnotation reads the recorded HTTP status back out of the span JSON:
// {"key":46,"value":{"Field":{"IntValue":500}}}.
func statusAnnotation(t *testing.T, tracer pinpoint.Tracer) int {
	t.Helper()
	annotations, _ := pptest.SpanOf(t, tracer)["Annotations"].([]interface{})
	for _, a := range annotations {
		m, _ := a.(map[string]interface{})
		if key, _ := m["key"].(float64); int(key) != pinpoint.AnnotationHttpStatusCode {
			continue
		}
		value, _ := m["value"].(map[string]interface{})
		field, _ := value["Field"].(map[string]interface{})
		n, _ := field["IntValue"].(float64)
		return int(n)
	}
	return 0
}

// A request that already carries the tracing headers - the filter added twice,
// say - gets a noop client tracer, and the filter has to end that one: ending the caller's tracer instead closed
// whatever event the caller had open.
func TestClientFilterChain_StackedFiltersLeaveTheCallersEventOpen(t *testing.T) {
	pptest.StartAgent(t)

	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/caller")
	defer tracer.EndSpan()
	tracer.NewSpanEvent("serverHandler")
	open := tracer.SpanEvent()

	next := func(context.Context, *httplib.BeegoHTTPRequest) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK}, nil
	}
	_, err := ClientFilterChain(tracer)(ClientFilterChain(tracer)(next))(context.Background(), httplib.Get("http://localhost:9090/hello"))
	require.NoError(t, err)

	assert.Same(t, open, tracer.SpanEvent(), "the stacked client filters ended the caller's own event")
}
