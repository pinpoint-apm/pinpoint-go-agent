package ppgrpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
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

// pinpointHeaders are the distributed tracing headers Inject writes; the callee
// continues the transaction from them.
var pinpointHeaders = []string{
	pinpoint.HeaderTraceId,
	pinpoint.HeaderSpanId,
	pinpoint.HeaderParentSpanId,
	pinpoint.HeaderParentApplicationName,
}

// lazyConn returns a *grpc.ClientConn that is never dialed: the client
// interceptors only read cc.Target() off it.
func lazyConn(t *testing.T, target string) *grpc.ClientConn {
	t.Helper()
	cc, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cc.Close() })
	return cc
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
			assert.Equal(t, tt.want, remoteAddr(ctx))
		})
	}

	// An interceptor can run without a peer - in tests, or over an in-process
	// transport - and must still produce an address.
	assert.Equal(t, "127.0.0.1", remoteAddr(context.Background()), "a call with no peer falls back")
}

func Test_makeUrl(t *testing.T) {
	assert.Equal(t, "grpc://localhost:8080/testapp.Hello/Greet",
		makeUrl("localhost:8080", "/testapp.Hello/Greet"))
}

// Incoming metadata is absent on an unary call made without any, and gRPC
// stores every key as a list. The reader has to flatten that to the single
// value the tracing header carries.
func Test_distributedTracingContextReaderMD(t *testing.T) {
	r := distributedTracingContextReaderMD{metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		pinpoint.HeaderTraceId, "txid^1^1",
		"multi", "first",
		"multi", "second",
	))}

	assert.Equal(t, "txid^1^1", r.Get(pinpoint.HeaderTraceId))
	assert.Equal(t, "first", r.Get("multi"), "only the first value of a repeated key is the header")
	assert.Equal(t, "", r.Get("absent"))

	// A context with no incoming metadata reads as absent, not a panic.
	assert.Equal(t, "", (distributedTracingContextReaderMD{context.Background()}).Get("any"))

	// gRPC lowercases metadata keys on the wire, so lookup has to match that.
	assert.Equal(t, "txid^1^1", r.Get(pinpoint.HeaderTraceId))
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
	require.True(t, ok, "no outgoing metadata on the returned context")
	assert.Equal(t, []string{"bearer token"}, md.Get("authorization"),
		"the application's own metadata must survive")
	for _, key := range pinpointHeaders {
		assert.NotEmpty(t, md.Get(key), "outgoing metadata is missing %s", key)
	}
	assert.Equal(t, tracer.TransactionId().String(), md.Get(pinpoint.HeaderTraceId)[0])
}

// The caller's own outgoing context must not be written to; only the derived
// one carries the tracing metadata.
func Test_newClientTracer_DoesNotModifyTheCallersMetadata(t *testing.T) {
	startAgent(t)

	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/caller")
	defer tracer.EndSpan()

	callerMD := metadata.Pairs("authorization", "bearer token")
	ctx := metadata.NewOutgoingContext(pinpoint.NewContext(context.Background(), tracer), callerMD)

	_, spanTracer := newClientTracer(ctx, "/testapp.Hello/Greet", "localhost:8080")
	defer spanTracer.EndSpanEvent()

	assert.Empty(t, callerMD.Get(pinpoint.HeaderTraceId),
		"the metadata the caller built was written to in place")
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

			assert.Equal(t, "/testapp.Hello/Greet", tracer.event.operation,
				"the span event is named after the gRPC method")
			assert.Equal(t, int32(pinpoint.ServiceTypeGrpc), tracer.event.serviceType)
			assert.Equal(t, tt.want, tracer.event.destination)
			assert.Equal(t, "grpc://"+tt.want+"/testapp.Hello/Greet",
				tracer.event.annotations[pinpoint.AnnotationHttpUrl])
			assert.True(t, tracer.event.ended, "the span event was left open")
		})
	}
}

// io.EOF is how a gRPC stream reports a clean end, so it must not be recorded
// as a failure; any other error must be.
func Test_endSpanEvent(t *testing.T) {
	for _, tt := range []struct {
		name    string
		err     error
		wantErr bool
	}{
		{name: "clean end", err: io.EOF},
		{name: "no error"},
		{name: "rpc failure", err: errors.New("unavailable"), wantErr: true},
		{name: "a wrapped io.EOF is still a failure", err: errWrappingEOF{}, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tracer := newRecordingTracer()
			tracer.NewSpanEvent("/testapp.Hello/Greet")

			endSpanEvent(tracer, tt.err)

			assert.Equal(t, tt.wantErr, tracer.event.err != nil, "recorded error = %v", tracer.event.err)
			assert.True(t, tracer.event.ended, "the span event was left open")
		})
	}
}

// errWrappingEOF stands for an error that carries io.EOF underneath but is not
// io.EOF itself; gRPC reports a clean stream end as io.EOF exactly.
type errWrappingEOF struct{}

func (errWrappingEOF) Error() string { return "wrapped: " + io.EOF.Error() }
func (errWrappingEOF) Unwrap() error { return io.EOF }

// A context without a span yields a noop tracer, and the interceptor still has
// to build an outgoing context rather than return the caller's unchanged.
func Test_newClientTracer_WithNoopTracer(t *testing.T) {
	newCtx, tracer := newClientTracer(context.Background(), "/testapp.Hello/Greet", "localhost:8080")
	defer tracer.EndSpanEvent()

	require.NotNil(t, tracer, "newClientTracer returned no tracer")
	assert.False(t, tracer.IsSampled(), "a context without a span produced a sampled tracer")

	md, ok := metadata.FromOutgoingContext(newCtx)
	require.True(t, ok, "no outgoing metadata on the returned context")
	assert.Equal(t, []string{"s0"}, md.Get(pinpoint.HeaderSampled),
		"an untraced call must tell the callee not to trace either")
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

	assert.Equal(t, int32(1), atomic.LoadInt32(&tracer.ends), "the span event must be closed exactly once")
}

// A successful send or receive is not the end of the stream, so it must not
// close the span event; only an error or CloseSend does.
func TestClientStream_SuccessfulCallsKeepTheSpanEventOpen(t *testing.T) {
	tracer := newCountingTracer()
	cs := &clientStream{ClientStream: &fakeClientStream{}, tracer: tracer}

	for i := 0; i < 3; i++ {
		require.NoError(t, cs.SendMsg(nil))
		require.NoError(t, cs.RecvMsg(nil))
	}
	require.Zero(t, atomic.LoadInt32(&tracer.ends), "the span event was closed before the stream ended")

	require.NoError(t, cs.CloseSend())
	assert.Equal(t, int32(1), atomic.LoadInt32(&tracer.ends), "CloseSend must close the span event")
}

// The stream's own error has to reach the caller unchanged, whichever call
// surfaces it.
func TestClientStream_ReturnsTheStreamError(t *testing.T) {
	want := errors.New("unavailable")
	tracer := newCountingTracer()
	cs := &clientStream{ClientStream: &fakeClientStream{err: want}, tracer: tracer}

	assert.ErrorIs(t, cs.SendMsg(nil), want)
	assert.ErrorIs(t, cs.RecvMsg(nil), want)
	assert.ErrorIs(t, cs.CloseSend(), want)
	assert.Equal(t, int32(1), atomic.LoadInt32(&tracer.ends), "the span event must be closed exactly once")
}

// The unary client interceptor is what an application actually installs: it has
// to hand the invoker a context carrying the tracing metadata and return the
// invoker's error unchanged.
func TestUnaryClientInterceptor(t *testing.T) {
	startAgent(t)

	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/caller")
	defer tracer.EndSpan()

	want := errors.New("unavailable")
	var invokedMD metadata.MD

	err := UnaryClientInterceptor()(
		pinpoint.NewContext(context.Background(), tracer),
		"/testapp.Hello/Greet", "request", "reply",
		lazyConn(t, "localhost:8080"),
		func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
			invokedMD, _ = metadata.FromOutgoingContext(ctx)
			return want
		})

	assert.ErrorIs(t, err, want, "the invoker's error must come back unchanged")
	require.NotNil(t, invokedMD, "the invoker was called without outgoing metadata")
	for _, key := range pinpointHeaders {
		assert.NotEmpty(t, invokedMD.Get(key), "outgoing metadata is missing %s", key)
	}
}

// The stream client interceptor wraps the stream the streamer returned, so the
// span event stays open until the stream ends.
func TestStreamClientInterceptor(t *testing.T) {
	startAgent(t)

	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/caller")
	defer tracer.EndSpan()

	var invokedMD metadata.MD
	stream, err := StreamClientInterceptor()(
		pinpoint.NewContext(context.Background(), tracer),
		&grpc.StreamDesc{StreamName: "Stream"},
		lazyConn(t, "localhost:8080"),
		"/testapp.Hello/Stream",
		func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			invokedMD, _ = metadata.FromOutgoingContext(ctx)
			return &fakeClientStream{}, nil
		})

	require.NoError(t, err)
	require.IsType(t, &clientStream{}, stream, "the returned stream must be the instrumented wrapper")
	for _, key := range pinpointHeaders {
		assert.NotEmpty(t, invokedMD.Get(key), "outgoing metadata is missing %s", key)
	}
	assert.NoError(t, stream.CloseSend())
}

// A streamer that fails never produces a stream, so the interceptor has to
// close its own span event and pass the error straight back.
func TestStreamClientInterceptor_StreamerError(t *testing.T) {
	startAgent(t)

	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/caller")
	defer tracer.EndSpan()

	want := errors.New("unavailable")
	stream, err := StreamClientInterceptor()(
		pinpoint.NewContext(context.Background(), tracer),
		&grpc.StreamDesc{StreamName: "Stream"},
		lazyConn(t, "localhost:8080"),
		"/testapp.Hello/Stream",
		func(context.Context, *grpc.StreamDesc, *grpc.ClientConn, string, ...grpc.CallOption) (grpc.ClientStream, error) {
			return nil, want
		})

	assert.ErrorIs(t, err, want)
	assert.Nil(t, stream, "a failed streamer must not yield a stream to wrap")
}

// forkingTracer records event pairing on itself and hands out a child tracer
// for NewGoroutineTracer, so a test can tell which tracer a span event was
// ended on.
type forkingTracer struct {
	pinpoint.Tracer
	ends      int32
	spanEnded bool
	child     *forkingTracer
}

func newForkingTracer() *forkingTracer { return &forkingTracer{Tracer: pinpoint.NoopTracer()} }

func (t *forkingTracer) IsSampled() bool                             { return true }
func (t *forkingTracer) NewSpanEvent(string) pinpoint.Tracer         { return t }
func (t *forkingTracer) EndSpanEvent()                               { atomic.AddInt32(&t.ends, 1) }
func (t *forkingTracer) EndSpan()                                    { t.spanEnded = true }
func (t *forkingTracer) NewGoroutineTracer() pinpoint.Tracer {
	t.child = newForkingTracer()
	return t.child
}

// The stream ends from whatever goroutine happens to drive it, so its lifetime
// must live on the dedicated goroutine tracer: ending it on the caller's
// tracer popped whatever event the application had open there at that moment
// and recorded the stream's error on it.
func TestStreamClientInterceptor_StreamEndsOnItsOwnTracer(t *testing.T) {
	startAgent(t)
	caller := newForkingTracer()

	stream, err := StreamClientInterceptor()(
		pinpoint.NewContext(context.Background(), caller),
		&grpc.StreamDesc{StreamName: "Stream"},
		lazyConn(t, "localhost:8080"),
		"/testapp.Hello/Stream",
		func(context.Context, *grpc.StreamDesc, *grpc.ClientConn, string, ...grpc.CallOption) (grpc.ClientStream, error) {
			return &fakeClientStream{err: io.EOF}, nil
		})
	require.NoError(t, err)

	assert.Equal(t, int32(1), atomic.LoadInt32(&caller.ends),
		"the interceptor must close the caller's event before handing the stream out")
	require.NotNil(t, caller.child, "the stream must run on its own goroutine tracer")

	_ = stream.RecvMsg(nil) // io.EOF: the stream is over

	assert.Equal(t, int32(1), atomic.LoadInt32(&caller.ends),
		"the stream's end must not touch the caller's tracer")
	assert.Equal(t, int32(1), atomic.LoadInt32(&caller.child.ends),
		"the stream's event must end on its own tracer")
	assert.True(t, caller.child.spanEnded, "the stream's goroutine span must be ended")
}

// A panicking invoker must still close the span event on its way up.
func TestUnaryClientInterceptor_PanicStillClosesTheSpanEvent(t *testing.T) {
	startAgent(t)
	caller := newForkingTracer()
	assert.PanicsWithValue(t, "boom", func() {
		_ = UnaryClientInterceptor()(
			pinpoint.NewContext(context.Background(), caller),
			"/testapp.Hello/Greet", "request", "reply",
			lazyConn(t, "localhost:8080"),
			func(context.Context, string, interface{}, interface{}, *grpc.ClientConn, ...grpc.CallOption) error {
				panic("boom")
			})
	})
	assert.Equal(t, int32(1), atomic.LoadInt32(&caller.ends), "the span event must be closed during panic unwinding")
}

// The interceptor wraps the handler, so the handler's result - value and error
// alike - has to come back untouched, with a sampled tracer in its context.
func TestUnaryServerInterceptor(t *testing.T) {
	startAgent(t)

	want := errors.New("handler failed")
	var tracer pinpoint.Tracer

	ctx := peer.NewContext(context.Background(),
		&peer.Peer{Addr: &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 50051}})

	resp, err := UnaryServerInterceptor()(
		ctx,
		"request",
		&grpc.UnaryServerInfo{FullMethod: "/testapp.Hello/Greet"},
		func(ctx context.Context, req interface{}) (interface{}, error) {
			tracer = pinpoint.FromContext(ctx)
			return "response", want
		})

	require.NotNil(t, tracer)
	assert.True(t, tracer.IsSampled(), "handler received an unsampled tracer")
	assert.Equal(t, "response", resp, "the handler's response must come back unchanged")
	assert.ErrorIs(t, err, want, "the handler's error must come back unchanged")

	span := spanOf(t, tracer)
	assert.Equal(t, "/testapp.Hello/Greet", span["RpcName"], "the span is named after the gRPC method")
	assert.Equal(t, "10.0.0.1", span["RemoteAddr"], "the peer address must be stripped of its port")
	assert.NotEqual(t, float64(0), span["Err"], "the handler error must fail the span")
}

// A handler that succeeds must leave the span unfailed.
func TestUnaryServerInterceptor_SuccessfulHandler(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	resp, err := UnaryServerInterceptor()(context.Background(), "request",
		&grpc.UnaryServerInfo{FullMethod: "/testapp.Hello/Greet"},
		func(ctx context.Context, req interface{}) (interface{}, error) {
			tracer = pinpoint.FromContext(ctx)
			return "response", nil
		})

	require.NoError(t, err)
	assert.Equal(t, "response", resp)
	assert.Equal(t, float64(0), spanOf(t, tracer)["Err"], "a successful handler must not fail the span")
}

// A gRPC server is usually one hop of a larger call: the tracing metadata the
// caller sent has to put this span in the caller's transaction.
func TestUnaryServerInterceptor_ContinuesTheCallersTransaction(t *testing.T) {
	startAgent(t)

	caller := pinpoint.GetAgent().NewSpanTracer("caller", "/caller")
	defer caller.EndSpan()

	// What the client interceptor would have put on the wire.
	outgoing, spanTracer := newClientTracer(
		pinpoint.NewContext(context.Background(), caller), "/testapp.Hello/Greet", "localhost:8080")
	spanTracer.EndSpanEvent()
	md, _ := metadata.FromOutgoingContext(outgoing)

	var callee pinpoint.Tracer
	_, err := UnaryServerInterceptor()(metadata.NewIncomingContext(context.Background(), md), "request",
		&grpc.UnaryServerInfo{FullMethod: "/testapp.Hello/Greet"},
		func(ctx context.Context, req interface{}) (interface{}, error) {
			callee = pinpoint.FromContext(ctx)
			return nil, nil
		})

	require.NoError(t, err)
	require.NotNil(t, callee)
	assert.Equal(t, caller.TransactionId().String(), callee.TransactionId().String())
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
		tracer   pinpoint.Tracer
		baseKept interface{}
		gotSrv   interface{}
	)

	srv := "the service implementation"
	err := StreamServerInterceptor()(
		srv,
		&fakeServerStream{ctx: base},
		&grpc.StreamServerInfo{FullMethod: "/testapp.Hello/Stream"},
		func(srv interface{}, stream grpc.ServerStream) error {
			tracer = pinpoint.FromContext(stream.Context())
			baseKept = stream.Context().Value(ctxKey{})
			gotSrv = srv
			return want
		})

	require.NotNil(t, tracer)
	assert.True(t, tracer.IsSampled(), "handler received an unsampled tracer")
	assert.Equal(t, "from-the-transport", baseKept, "the transport's context values were discarded")
	assert.Equal(t, srv, gotSrv, "the service implementation must reach the handler unchanged")
	assert.ErrorIs(t, err, want, "the handler's error must come back unchanged")
	assert.Equal(t, "/testapp.Hello/Stream", spanOf(t, tracer)["RpcName"])
	assert.NotEqual(t, float64(0), spanOf(t, tracer)["Err"], "the handler error must fail the span")
}

// The stream interceptor reads its tracing metadata off the stream's context,
// so it continues the caller's transaction the same way the unary one does.
func TestStreamServerInterceptor_ContinuesTheCallersTransaction(t *testing.T) {
	startAgent(t)

	caller := pinpoint.GetAgent().NewSpanTracer("caller", "/caller")
	defer caller.EndSpan()

	outgoing, spanTracer := newClientTracer(
		pinpoint.NewContext(context.Background(), caller), "/testapp.Hello/Stream", "localhost:8080")
	spanTracer.EndSpanEvent()
	md, _ := metadata.FromOutgoingContext(outgoing)

	var callee pinpoint.Tracer
	err := StreamServerInterceptor()(nil,
		&fakeServerStream{ctx: metadata.NewIncomingContext(context.Background(), md)},
		&grpc.StreamServerInfo{FullMethod: "/testapp.Hello/Stream"},
		func(srv interface{}, stream grpc.ServerStream) error {
			callee = pinpoint.FromContext(stream.Context())
			return nil
		})

	require.NoError(t, err)
	require.NotNil(t, callee)
	assert.Equal(t, caller.TransactionId().String(), callee.TransactionId().String())
}

// A panicking handler must not be swallowed by either server interceptor.
func TestServerInterceptors_PanicPropagates(t *testing.T) {
	startAgent(t)

	assert.PanicsWithValue(t, "boom", func() {
		_, _ = UnaryServerInterceptor()(context.Background(), nil,
			&grpc.UnaryServerInfo{FullMethod: "/testapp.Hello/Greet"},
			func(context.Context, interface{}) (interface{}, error) { panic("boom") })
	})

	assert.PanicsWithValue(t, "boom", func() {
		_ = StreamServerInterceptor()(nil, &fakeServerStream{ctx: context.Background()},
			&grpc.StreamServerInfo{FullMethod: "/testapp.Hello/Stream"},
			func(interface{}, grpc.ServerStream) error { panic("boom") })
	})
}

// With no agent running the server interceptors must be straight
// pass-throughs.
func TestServerInterceptors_PassThroughWhenAgentDisabled(t *testing.T) {
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

	called := false
	_, err := UnaryServerInterceptor()(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/testapp.Hello/Greet"},
		func(ctx context.Context, req interface{}) (interface{}, error) {
			called = true
			assert.False(t, pinpoint.FromContext(ctx).IsSampled(), "a disabled agent produced a sampled tracer")
			return nil, nil
		})
	require.NoError(t, err)
	assert.True(t, called, "the unary handler did not run")

	called = false
	err = StreamServerInterceptor()(nil, &fakeServerStream{ctx: context.Background()},
		&grpc.StreamServerInfo{FullMethod: "/testapp.Hello/Stream"},
		func(srv interface{}, stream grpc.ServerStream) error {
			called = true
			assert.False(t, pinpoint.FromContext(stream.Context()).IsSampled(),
				"a disabled agent produced a sampled tracer")
			return nil
		})
	require.NoError(t, err)
	assert.True(t, called, "the stream handler did not run")
}

// serverStream must delegate everything but Context to the stream gRPC gave it.
func Test_serverStream(t *testing.T) {
	type ctxKey struct{}
	base := context.WithValue(context.Background(), ctxKey{}, "from-the-transport")
	wrapped := context.WithValue(base, ctxKey{}, "from-the-interceptor")

	s := &serverStream{ServerStream: &fakeServerStream{ctx: base}, context: wrapped}

	assert.Equal(t, "from-the-interceptor", s.Context().Value(ctxKey{}),
		"Context must report the interceptor's context, not the transport's")
}
