package ppkratosv3

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-kratos/kratos/v3/transport"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

func TestClientMiddleware_GRPCOutgoingMetadata(t *testing.T) {
	for _, mode := range []string{"forwarded", "stale sampling", "outer instrumentation", "no tracer"} {
		t.Run(mode, func(t *testing.T) {
			agent := pptest.StartAgent(t)
			upstream := agent.NewSpanTracer("upstream", "/upstream")
			defer upstream.EndSpan()
			headers := http.Header{}
			upstream.NewSpanEvent("send")
			upstream.Inject(headers)
			upstream.EndSpanEvent()
			caller := agent.NewSpanTracerWithReader("gateway", "/gateway", pinpoint.HttpHeaderReader(headers))
			defer caller.EndSpan()
			tracer := caller
			switch mode {
			case "stale sampling":
				headers = http.Header{}
				headers.Set(pinpoint.HeaderSampled, "s0")
				headers.Set(pinpoint.HeaderParentApplicationNamespace, "old-cluster")
			case "outer instrumentation":
				headers = http.Header{}
				caller.NewSpanEvent("outer")
				defer caller.EndSpanEvent()
				caller.Inject(headers)
			case "no tracer":
				tracer = pinpoint.NoopTracer()
			}
			original := metadata.MD{}
			for key, values := range headers {
				original.Set(key, values...)
			}
			original.Set("x-app", "first", "second")
			snapshot := original.Copy()
			ctx := pinpoint.NewContext(metadata.NewOutgoingContext(context.Background(), original), tracer)
			carrier := newGrpcTransport("localhost:9000", "/service/Call")
			ctx = transport.NewClientContext(ctx, carrier)
			var sent metadata.MD
			invoke := ClientMiddleware()(ClientMiddleware()(func(ctx context.Context, _ interface{}) (interface{}, error) {
				// Kratos appends its request carrier to the context metadata before invoking gRPC.
				var pairs []string
				for key, value := range carrier.reqHeader.m {
					pairs = append(pairs, key, value)
				}
				sent, _ = metadata.FromOutgoingContext(metadata.AppendToOutgoingContext(ctx, pairs...))
				return nil, nil
			}))
			_, err := invoke(ctx, nil)
			require.NoError(t, err)
			assert.Equal(t, snapshot, original, "the caller's metadata must not be changed")
			assert.Equal(t, snapshot.Get("x-app"), sent.Get("x-app"))
			if mode == "outer instrumentation" || mode == "no tracer" {
				assert.Empty(t, carrier.reqHeader.m)
			}
			if mode == "no tracer" {
				assert.Equal(t, snapshot, sent)
				return
			}
			require.Len(t, sent.Get(pinpoint.HeaderSpanId), 1)
			assert.Empty(t, sent.Get(pinpoint.HeaderSampled))
			assert.Empty(t, sent.Get(pinpoint.HeaderParentApplicationNamespace))
			callee := agent.NewSpanTracerWithReader("callee", "/callee", outgoingMetadataReader(sent))
			defer callee.EndSpan()
			assert.True(t, callee.IsSampled())
			assert.Equal(t, caller.TransactionId(), callee.TransactionId())
			assert.NotEqual(t, caller.SpanId(), callee.SpanId())
		})
	}
}
