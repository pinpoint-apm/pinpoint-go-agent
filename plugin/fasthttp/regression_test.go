package ppfasthttp

import (
	"bufio"
	"net/http"
	"testing"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

// A handler wrapped twice - a router adapter's route inside a manual
// WrapHandler - makes one span: the inner layer finds the tracer the outer one
// stored under CtxKey and records on it.
func TestWrapHandler_WrappedTwiceIsNested(t *testing.T) {
	startAgent(t)

	var outer, inner pinpoint.Tracer
	h := WrapHandler(func(ctx *fasthttp.RequestCtx) {
		outer = tracerOf(t, ctx)
		WrapHandler(func(ctx *fasthttp.RequestCtx) {
			inner = tracerOf(t, ctx)
		})(ctx)
	})
	h(newRequestCtx(http.MethodGet, "http://localhost/hello"))

	require.NotNil(t, outer)
	require.NotNil(t, inner)
	assert.False(t, pinpoint.IsNestedTracer(outer))
	assert.True(t, pinpoint.IsNestedTracer(inner), "the inner layer records on the outer span")
	assert.Equal(t, outer.SpanId(), inner.SpanId(), "one span per request")
	assert.Equal(t, outer.TransactionId(), inner.TransactionId())
}

// RequestCtx.Err() turns non-nil once Server.Shutdown has begun, for every
// request still in flight; it is not a handler error, and recording it marked
// each request that completed during a graceful stop as failed.
func TestWrapHandler_ShutdownDoesNotFailInflightRequests(t *testing.T) {
	startAgent(t)

	entered := make(chan struct{})
	release := make(chan struct{})
	var tracer pinpoint.Tracer
	s := &fasthttp.Server{Handler: WrapHandler(func(ctx *fasthttp.RequestCtx) {
		tracer = tracerOf(t, ctx)
		close(entered)
		<-release
		ctx.SetStatusCode(http.StatusOK)
		ctx.SetBodyString("ok")
	})}
	ln := fasthttputil.NewInmemoryListener()
	go func() { _ = s.Serve(ln) }()

	conn, err := ln.Dial()
	require.NoError(t, err)
	_, err = conn.Write([]byte("GET /hello HTTP/1.1\r\nHost: x\r\n\r\n"))
	require.NoError(t, err)
	<-entered

	shut := make(chan error, 1)
	go func() { shut <- s.Shutdown() }()
	// Give Shutdown time to close the server's done channel, which is what
	// makes RequestCtx.Err() report context.Canceled.
	time.Sleep(100 * time.Millisecond)
	close(release)

	var resp fasthttp.Response
	require.NoError(t, resp.Read(bufio.NewReader(conn)))
	require.NoError(t, <-shut)

	assert.Equal(t, http.StatusOK, resp.StatusCode())
	assert.Equal(t, float64(0), spanOf(t, tracer)["Err"], "a request that completed during shutdown is not a failure")
}

// A RequestCtx no server initialized - what a unit test of a handler builds -
// has no server to read a shutdown from, and the wrapper must not reach for
// one.
func TestWrapHandler_BareRequestCtx(t *testing.T) {
	startAgent(t)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(http.MethodGet)
	ctx.Request.SetRequestURI("http://localhost/hello")

	called := false
	assert.NotPanics(t, func() {
		WrapHandler(func(ctx *fasthttp.RequestCtx) { called = true })(ctx)
	})
	assert.True(t, called)
}
