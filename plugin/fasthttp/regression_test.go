package ppfasthttp

import (
	"net/http"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
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
