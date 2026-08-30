package ppgrpc

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
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

// The remote address recorded on a server span comes from the gRPC peer, which
// carries a transport address rather than a host. Anything that is not
// host:port has to fall back rather than record a port or an empty string.
func Test_remoteAddr(t *testing.T) {
	for _, tt := range []struct {
		name string
		addr net.Addr
		want string
	}{
		{"tcp4", &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 50051}, "10.0.0.1"},
		{"tcp6", &net.TCPAddr{IP: net.ParseIP("::1"), Port: 50051}, "::1"},
		// A unix socket path has no port, so SplitHostPort fails.
		{"unix socket", &net.UnixAddr{Name: "/tmp/grpc.sock", Net: "unix"}, "127.0.0.1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: tt.addr})
			if got := remoteAddr(ctx); got != tt.want {
				t.Errorf("remoteAddr() = %q, want %q", got, tt.want)
			}
		})
	}

	// An interceptor can run without a peer - in tests, or over an in-process
	// transport - and must still produce an address.
	if got := remoteAddr(context.Background()); got != "127.0.0.1" {
		t.Errorf("remoteAddr(no peer) = %q, want 127.0.0.1", got)
	}
}

func Test_makeUrl(t *testing.T) {
	if got, want := makeUrl("localhost:8080", "/testapp.Hello/Greet"), "grpc://localhost:8080/testapp.Hello/Greet"; got != want {
		t.Errorf("makeUrl() = %q, want %q", got, want)
	}
}

// Incoming metadata is absent on an unary call made without any, and gRPC
// stores every key as a list. The reader has to flatten that to the single
// value the tracing header carries.
func Test_distributedTracingContextReaderMD(t *testing.T) {
	r := distributedTracingContextReaderMD{metadata.Pairs(
		pinpoint.HeaderTraceId, "txid^1^1",
		"multi", "first",
		"multi", "second",
	)}

	if got := r.Get(pinpoint.HeaderTraceId); got != "txid^1^1" {
		t.Errorf("Get(%s) = %q, want %q", pinpoint.HeaderTraceId, got, "txid^1^1")
	}
	if got := r.Get("multi"); got != "first" {
		t.Errorf("Get(multi) = %q, want %q", got, "first")
	}
	if got := r.Get("absent"); got != "" {
		t.Errorf("Get(absent) = %q, want empty", got)
	}

	// metadata.FromIncomingContext returns a nil map when there is none.
	if got := (distributedTracingContextReaderMD{nil}).Get("any"); got != "" {
		t.Errorf("Get on nil metadata = %q, want empty", got)
	}
}

// The client interceptor has to publish the tracing headers as outgoing
// metadata - that is the only channel the callee can read them from - without
// dropping metadata the application already set.
func Test_newClientTracer_InjectsMetadata(t *testing.T) {
	startAgent(t)

	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/caller")
	defer tracer.EndSpan()

	ctx := metadata.AppendToOutgoingContext(
		pinpoint.NewContext(context.Background(), tracer), "authorization", "bearer token")

	newCtx, spanTracer := newClientTracer(ctx, "/testapp.Hello/Greet", "localhost:8080")
	defer spanTracer.EndSpanEvent()

	md, ok := metadata.FromOutgoingContext(newCtx)
	if !ok {
		t.Fatal("no outgoing metadata on the returned context")
	}
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "bearer token" {
		t.Errorf("application metadata = %q, want [bearer token]", got)
	}
	for _, key := range []string{
		pinpoint.HeaderTraceId,
		pinpoint.HeaderSpanId,
		pinpoint.HeaderParentSpanId,
		pinpoint.HeaderParentApplicationName,
	} {
		if len(md.Get(key)) == 0 {
			t.Errorf("outgoing metadata is missing %s", key)
		}
	}
}

// recordingTracer captures what the instrumentation records on a span event.
// A real tracer's recorders are write-only, so this stands in for one wherever
// a test asserts recorded values rather than observable behaviour.
type recordingTracer struct {
	pinpoint.Tracer
	event *recordedEvent
}

func newRecordingTracer() *recordingTracer {
	return &recordingTracer{Tracer: pinpoint.NoopTracer()}
}

func (t *recordingTracer) IsSampled() bool { return true }

func (t *recordingTracer) NewSpanEvent(operation string) pinpoint.Tracer {
	t.event = &recordedEvent{
		SpanEventRecorder: t.Tracer.SpanEvent(),
		operation:         operation,
		annotations:       map[int32]string{},
	}
	return t
}

func (t *recordingTracer) SpanEvent() pinpoint.SpanEventRecorder { return t.event }

func (t *recordingTracer) EndSpanEvent() { t.event.ended = true }

type recordedEvent struct {
	pinpoint.SpanEventRecorder
	operation   string
	serviceType int32
	destination string
	err         error
	annotations map[int32]string
	ended       bool
}

func (e *recordedEvent) SetServiceType(typ int32)        { e.serviceType = typ }
func (e *recordedEvent) SetDestination(id string)        { e.destination = id }
func (e *recordedEvent) SetError(err error, _ ...string) { e.err = err }

func (e *recordedEvent) Annotations() pinpoint.Annotation {
	return recordedAnnotation{Annotation: e.SpanEventRecorder.Annotations(), into: e.annotations}
}

type recordedAnnotation struct {
	pinpoint.Annotation
	into map[int32]string
}

func (a recordedAnnotation) AppendString(key int32, s string) { a.into[key] = s }

// The destination recorded for a client call is the dial target, which gRPC
// spells with a resolver scheme. Recording it verbatim would file one server
// under several names, and the recorded URL has to match it.
func Test_newClientTracer_RecordsTheDialTarget(t *testing.T) {
	for _, tt := range []struct {
		target string
		want   string
	}{
		{"localhost:8080", "localhost:8080"},
		{"dns:///localhost:8080", "localhost:8080"},
		// A unix target is a socket path, not an address to group servers by.
		{"unix:/tmp/grpc.sock", "localhost"},
		{"unix:///tmp/grpc.sock", "localhost"},
	} {
		t.Run(tt.target, func(t *testing.T) {
			tracer := newRecordingTracer()

			_, spanTracer := newClientTracer(
				pinpoint.NewContext(context.Background(), tracer), "/testapp.Hello/Greet", tt.target)
			spanTracer.EndSpanEvent()

			if tracer.event.operation != "/testapp.Hello/Greet" {
				t.Errorf("operation = %q, want %q", tracer.event.operation, "/testapp.Hello/Greet")
			}
			if tracer.event.serviceType != pinpoint.ServiceTypeGrpc {
				t.Errorf("service type = %d, want %d", tracer.event.serviceType, pinpoint.ServiceTypeGrpc)
			}
			if tracer.event.destination != tt.want {
				t.Errorf("destination = %q, want %q", tracer.event.destination, tt.want)
			}
			if got, want := tracer.event.annotations[pinpoint.AnnotationHttpUrl], "grpc://"+tt.want+"/testapp.Hello/Greet"; got != want {
				t.Errorf("url annotation = %q, want %q", got, want)
			}
			if !tracer.event.ended {
				t.Error("the span event was left open")
			}
		})
	}
}

// io.EOF is how a gRPC stream reports a clean end, so it must not be recorded
// as a failure; any other error must be.
func Test_endSpanEvent(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want error
	}{
		{"clean end", io.EOF, nil},
		{"no error", nil, nil},
		{"rpc failure", errors.New("unavailable"), errors.New("unavailable")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tracer := newRecordingTracer()
			tracer.NewSpanEvent("/testapp.Hello/Greet")

			endSpanEvent(tracer, tt.err)

			if (tracer.event.err == nil) != (tt.want == nil) {
				t.Errorf("recorded error = %v, want %v", tracer.event.err, tt.want)
			}
			if !tracer.event.ended {
				t.Error("the span event was left open")
			}
		})
	}
}

// A context without a span yields a noop tracer, and the interceptor still has
// to build an outgoing context rather than return the caller's unchanged.
func Test_newClientTracer_WithNoopTracer(t *testing.T) {
	newCtx, tracer := newClientTracer(context.Background(), "/testapp.Hello/Greet", "localhost:8080")
	defer tracer.EndSpanEvent()

	if tracer == nil {
		t.Fatal("newClientTracer returned no tracer")
	}
	if tracer.IsSampled() {
		t.Error("a context without a span produced a sampled tracer")
	}
	if _, ok := metadata.FromOutgoingContext(newCtx); !ok {
		t.Error("no outgoing metadata on the returned context")
	}
}

type countingTracer struct {
	pinpoint.Tracer
	ends int32
}

func newCountingTracer() *countingTracer { return &countingTracer{Tracer: pinpoint.NoopTracer()} }

func (t *countingTracer) EndSpanEvent() { atomic.AddInt32(&t.ends, 1) }

type fakeClientStream struct {
	grpc.ClientStream
	err error
}

func (s *fakeClientStream) SendMsg(interface{}) error { return s.err }
func (s *fakeClientStream) RecvMsg(interface{}) error { return s.err }
func (s *fakeClientStream) CloseSend() error          { return s.err }
func (s *fakeClientStream) Context() context.Context  { return context.Background() }

// A gRPC stream is legally used from two goroutines at once - one sending, one
// receiving - and either side can be the one that sees the stream end. The
// span event must be closed exactly once no matter how the race falls, or the
// agent's event stack unwinds too far. Run under -race.
func TestClientStream_EndsTheSpanEventOnce(t *testing.T) {
	tracer := newCountingTracer()
	cs := &clientStream{ClientStream: &fakeClientStream{err: io.EOF}, tracer: tracer}

	var wg sync.WaitGroup
	for _, call := range []func() error{
		func() error { return cs.SendMsg(nil) },
		func() error { return cs.RecvMsg(nil) },
		cs.CloseSend,
	} {
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(call func() error) {
				defer wg.Done()
				_ = call()
			}(call)
		}
	}
	wg.Wait()

	if got := atomic.LoadInt32(&tracer.ends); got != 1 {
		t.Errorf("EndSpanEvent called %d times, want 1", got)
	}
}

// A successful send or receive is not the end of the stream, so it must not
// close the span event; only an error or CloseSend does.
func TestClientStream_SuccessfulCallsKeepTheSpanEventOpen(t *testing.T) {
	tracer := newCountingTracer()
	cs := &clientStream{ClientStream: &fakeClientStream{}, tracer: tracer}

	for i := 0; i < 3; i++ {
		if err := cs.SendMsg(nil); err != nil {
			t.Fatalf("SendMsg() = %v", err)
		}
		if err := cs.RecvMsg(nil); err != nil {
			t.Fatalf("RecvMsg() = %v", err)
		}
	}
	if got := atomic.LoadInt32(&tracer.ends); got != 0 {
		t.Fatalf("EndSpanEvent called %d times before the stream ended", got)
	}

	if err := cs.CloseSend(); err != nil {
		t.Fatalf("CloseSend() = %v", err)
	}
	if got := atomic.LoadInt32(&tracer.ends); got != 1 {
		t.Errorf("EndSpanEvent called %d times after CloseSend, want 1", got)
	}
}

// The interceptor wraps the handler, so the handler's result - value and error
// alike - has to come back untouched, with a sampled tracer in its context.
func TestUnaryServerInterceptor(t *testing.T) {
	startAgent(t)

	want := errors.New("handler failed")
	var sampled bool

	resp, err := UnaryServerInterceptor()(
		context.Background(),
		"request",
		&grpc.UnaryServerInfo{FullMethod: "/testapp.Hello/Greet"},
		func(ctx context.Context, req interface{}) (interface{}, error) {
			sampled = pinpoint.FromContext(ctx).IsSampled()
			return "response", want
		})

	if !sampled {
		t.Error("handler received an unsampled tracer")
	}
	if resp != "response" {
		t.Errorf("response = %v, want %q", resp, "response")
	}
	if !errors.Is(err, want) {
		t.Errorf("error = %v, want %v", err, want)
	}
}

type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *fakeServerStream) Context() context.Context { return s.ctx }

// The stream interceptor cannot replace the handler's context argument - there
// is none - so it has to hand the handler a stream whose Context carries the
// tracer, while leaving everything else about the stream alone.
func TestStreamServerInterceptor(t *testing.T) {
	startAgent(t)

	type ctxKey struct{}
	base := context.WithValue(context.Background(), ctxKey{}, "from-the-transport")

	want := errors.New("handler failed")
	var (
		sampled  bool
		baseKept interface{}
	)

	err := StreamServerInterceptor()(
		nil,
		&fakeServerStream{ctx: base},
		&grpc.StreamServerInfo{FullMethod: "/testapp.Hello/Stream"},
		func(srv interface{}, stream grpc.ServerStream) error {
			sampled = pinpoint.FromContext(stream.Context()).IsSampled()
			baseKept = stream.Context().Value(ctxKey{})
			return want
		})

	if !sampled {
		t.Error("handler received an unsampled tracer")
	}
	if baseKept != "from-the-transport" {
		t.Errorf("transport context value = %v, want %q", baseKept, "from-the-transport")
	}
	if !errors.Is(err, want) {
		t.Errorf("error = %v, want %v", err, want)
	}
}

// With no agent running the server interceptors must be straight
// pass-throughs.
func TestServerInterceptors_PassThroughWhenAgentDisabled(t *testing.T) {
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

	called := false
	if _, err := UnaryServerInterceptor()(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/testapp.Hello/Greet"},
		func(ctx context.Context, req interface{}) (interface{}, error) {
			called = true
			if pinpoint.FromContext(ctx).IsSampled() {
				t.Error("a disabled agent produced a sampled tracer")
			}
			return nil, nil
		}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("the unary handler did not run")
	}

	called = false
	if err := StreamServerInterceptor()(nil, &fakeServerStream{ctx: context.Background()},
		&grpc.StreamServerInfo{FullMethod: "/testapp.Hello/Stream"},
		func(srv interface{}, stream grpc.ServerStream) error {
			called = true
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("the stream handler did not run")
	}
}
