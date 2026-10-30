package ppgrpc

import (
	"context"
	"io"
	"strings"
	"sync"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type clientStream struct {
	grpc.ClientStream
	once   sync.Once
	tracer pinpoint.Tracer
}

func (cs *clientStream) SendMsg(m interface{}) error {
	err := cs.ClientStream.SendMsg(m)
	if err != nil && err != io.EOF { // only the bare sentinel is a clean end; a wrapped EOF is a failure
		cs.endSpan(err)
	}
	return err
}

func (cs *clientStream) RecvMsg(m interface{}) error {
	err := cs.ClientStream.RecvMsg(m)
	if err != nil {
		cs.endSpan(err)
	}
	return err
}

func (cs *clientStream) CloseSend() error {
	err := cs.ClientStream.CloseSend()
	// Closing the send direction still allows responses and a final status.
	if err != nil {
		cs.endSpan(err)
	}
	return err
}

func (cs *clientStream) endSpan(err error) {
	cs.once.Do(func() {
		endSpanEvent(cs.tracer, err)
		cs.tracer.EndSpan()
	})
}

// kvInjectionWriter collects the injected headers so they go out through one
// metadata.AppendToOutgoingContext call. The keys are stored pre-lowered:
// AppendToOutgoingContext runs strings.ToLower over every key it is given,
// which allocates a new string for each of the mixed-case pinpoint names and
// returns a lowercase one as it is.
type kvInjectionWriter struct {
	kv []string
	// buf backs kv so the writer and its slice are one allocation; the ten
	// pinpoint headers fit, and a larger set simply grows kv onto the heap.
	buf [2 * 10]string
}

func (w *kvInjectionWriter) Set(key string, value string) {
	w.kv = append(w.kv, loweredKey(key), value)
}

func newClientTracer(ctx context.Context, method string, target string) (context.Context, pinpoint.Tracer) {
	tracer := pinpoint.FromContext(ctx)
	ctx, nested := pinpoint.OutgoingGrpcContext(ctx, tracer)
	if nested {
		return ctx, pinpoint.NoopTracer()
	}
	tracer = tracer.NewSpanEvent(method)
	if tracer.IsSampled() {
		se := tracer.SpanEvent()
		se.SetServiceType(pinpoint.ServiceTypeGrpc)

		//refer https://github.com/grpc/grpc/blob/master/doc/naming.md
		var remote string
		if strings.HasPrefix(target, "unix:") {
			remote = "localhost"
		} else {
			remote = strings.TrimPrefix(target, "dns:///")
		}
		se.SetDestination(remote)
		se.Annotations().AppendString(pinpoint.AnnotationHttpUrl, makeUrl(remote, method))
	}

	writer := &kvInjectionWriter{}
	writer.kv = writer.buf[:0]
	tracer.Inject(writer)
	if len(writer.kv) > 0 {
		ctx = metadata.AppendToOutgoingContext(ctx, writer.kv...)
	}

	return ctx, tracer
}

func makeUrl(remote string, method string) string {
	return "grpc://" + remote + method
}

func endSpanEvent(tracer pinpoint.Tracer, err error) {
	defer tracer.EndSpanEvent()
	if err != nil && err != io.EOF { // only the bare sentinel is a clean end; a wrapped EOF is a failure
		tracer.SpanEvent().SetError(err, "grpc error")
	}
}

// UnaryClientInterceptor returns a new grpc.UnaryClientInterceptor ready to instrument.
func UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) (err error) {
		if !pinpoint.GetAgent().Enable() {
			return invoker(ctx, method, req, reply, cc, opts...)
		}

		newCtx, tracer := newClientTracer(ctx, method, cc.Target())
		// Deferred so a panicking invoker still closes the span event.
		defer func() { endSpanEvent(tracer, err) }()
		err = invoker(newCtx, method, req, reply, cc, opts...)
		return err
	}
}

// StreamClientInterceptor returns a new grpc.StreamClientInterceptor ready to instrument.
func StreamClientInterceptor() grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (_ grpc.ClientStream, err error) {
		if !pinpoint.GetAgent().Enable() {
			return streamer(ctx, desc, cc, method, opts...)
		}

		newCtx, tracer := newClientTracer(ctx, method, cc.Target())
		// Deferred so a panicking streamer still closes the creation event; on
		// success it closes the event LIFO-correctly, right before the stream
		// is handed out.
		defer func() { endSpanEvent(tracer, err) }()

		// The stream outlives this call, and gRPC explicitly allows driving it
		// from other goroutines (one sending, one receiving), so its lifetime
		// is traced on its own goroutine tracer. Ending the interceptor's
		// event from a stream goroutine instead popped whatever event the
		// application had open on the caller's tracer at that moment and
		// recorded the stream's error on it.
		streamTracer := tracer.NewGoroutineTracer()
		streamTracer.NewSpanEvent(method)
		if streamTracer.IsSampled() {
			streamTracer.SpanEvent().SetServiceType(pinpoint.ServiceTypeGrpc)
		}

		cs := &clientStream{tracer: streamTracer}
		// Initialize the tracer before calling the streamer: OnFinish may run
		// before the stream is returned. It reports the actual RPC status, whereas
		// the stream context is canceled on success too. It also ends abandoned
		// streams without a goroutine waiting on Context().Done().
		//
		// Capped before the append: with no per-call options gRPC hands every
		// stream the ClientConn's own default option slice, and an append into
		// its spare capacity raced other streams and gave them this stream's
		// OnFinish.
		opts = append(opts[:len(opts):len(opts)], grpc.OnFinish(cs.endSpan))
		var stream grpc.ClientStream
		defer func() {
			if stream == nil {
				cs.endSpan(err)
			}
		}()
		stream, err = streamer(newCtx, desc, cc, method, opts...)
		if err != nil {
			return nil, err
		}
		cs.ClientStream = stream
		return cs, nil
	}
}
