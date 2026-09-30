package ppkratosv3

import (
	"context"
	"testing"

	"github.com/go-kratos/kratos/v3/transport"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The middleware registered twice - globally and per service - or a kratos
// gRPC server that also runs ppgrpc's interceptor makes one span: the inner
// layer finds the tracer the outer one put in the context and records on it.
func TestServerMiddleware_InsideAnotherTracerIsNested(t *testing.T) {
	startAgent(t)

	mw := ServerMiddleware()
	var outer, inner pinpoint.Tracer
	_, err := mw(func(ctx context.Context, req interface{}) (interface{}, error) {
		outer = pinpoint.FromContext(ctx)
		return mw(func(ctx context.Context, req interface{}) (interface{}, error) {
			inner = pinpoint.FromContext(ctx)
			return "reply", nil
		})(ctx, req)
	})(transport.NewServerContext(context.Background(),
		newGrpcTransport("grpc://127.0.0.1:9000", "/helloworld.Greeter/SayHello")), "request")

	require.NoError(t, err)
	require.NotNil(t, outer)
	require.NotNil(t, inner)
	assert.False(t, pinpoint.IsNestedTracer(outer), "the outer middleware owns the span")
	assert.True(t, pinpoint.IsNestedTracer(inner), "the inner middleware records on the outer span")
	assert.Equal(t, outer.SpanId(), inner.SpanId(), "one span per request")
	assert.Equal(t, outer.TransactionId(), inner.TransactionId())
	assert.Equal(t, "127.0.0.1:9000", spanOf(t, outer)["EndPoint"], "the owner recorded the endpoint")
}
