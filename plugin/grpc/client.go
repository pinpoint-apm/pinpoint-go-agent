package ppgrpc

import (
	"context"
	"io"
	"strings"
	"sync"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type clientStream struct {
	grpc.ClientStream
	mutex      sync.Mutex
	isFinished bool
	tracer     pinpoint.Tracer
}

func (cs *clientStream) SendMsg(m interface{}) error {
	err := cs.ClientStream.SendMsg(m)
	if err != nil {
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
	cs.endSpan(err)
	return err
}

func (cs *clientStream) endSpan(err error) {
	cs.mutex.Lock()
	defer cs.mutex.Unlock()
	if !cs.isFinished {
		endSpanEvent(cs.tracer, err)
		cs.tracer.EndSpan()
		cs.isFinished = true
	}
}

// kvInjectionWriter collects the injected headers so they go out through one
// metadata.AppendToOutgoingContext call rather than a lowercase allocation per
// Set.
type kvInjectionWriter struct {
	kv []string
	// buf backs kv so the writer and its slice are one allocation; the ten
	// pinpoint headers fit, and a larger set simply grows kv onto the heap.
	buf [2 * 10]string
}

func (w *kvInjectionWriter) Set(key string, value string) {
	w.kv = append(w.kv, key, value)
}

func newClientTracer(ctx context.Context, method string, target string) (context.Context, pinpoint.Tracer) {
	if isNested(ctx) {
		return ctx, pinpoint.NoopTracer()
	}
	tracer := pinpoint.FromContext(ctx).NewSpanEvent(method)
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

// isNested reports whether the caller's outgoing metadata already carries a
// Pinpoint trace context - an outer instrumented layer, an interceptor
// registered twice, or a gateway forwarding its inbound metadata with
// metadata.NewOutgoingContext. Java's DefaultRequestTraceWriter.isNested then
// records no span event and writes no header, so the context already present
// travels alone; appending a second value left the receiver's Get reading the
// first, stale, one. Keys are checked as Java does, by the presence of
// Pinpoint-TraceID or Pinpoint-Sampled.
//
// FromOutgoingContext copies the caller's MD: a cost per call that only exists
// when the caller set metadata of its own.
func isNested(ctx context.Context) bool {
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		return false
	}
	return len(md[loweredHeaderKeys[pinpoint.HeaderTraceId]]) > 0 ||
		len(md[loweredHeaderKeys[pinpoint.HeaderSampled]]) > 0
}

func makeUrl(remote string, method string) string {
	return "grpc://" + remote + method
}

func endSpanEvent(tracer pinpoint.Tracer, err error) {
	defer tracer.EndSpanEvent()
	if err != nil && err != io.EOF {
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

		stream, err := streamer(newCtx, desc, cc, method, opts...)
		if err != nil {
			return nil, err
		}

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

		cs := &clientStream{ClientStream: stream, tracer: streamTracer}

		// A stream the caller abandons - its context cancelled, or a deadline
		// reached - makes no further Recv or CloseSend, so nothing ended its
		// span and the whole async span was lost. gRPC cancels the stream's
		// context on every termination path, and endSpan is idempotent, so
		// this only does the work the methods above did not already do.
		go func() {
			<-stream.Context().Done()
			cs.endSpan(stream.Context().Err())
		}()

		return cs, nil
	}
}
