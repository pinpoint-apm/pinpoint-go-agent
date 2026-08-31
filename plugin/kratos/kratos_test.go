package ppkratos

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-kratos/kratos/v2/transport"
	transhttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// header is a transport.Header backed by a map, standing in for the http.Header
// or metadata.MD a real transport carries.
type header map[string][]string

func (h header) Get(key string) string {
	if v := h[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}
func (h header) Set(key, value string) { h[key] = []string{value} }
func (h header) Add(key, value string) { h[key] = append(h[key], value) }
func (h header) Values(key string) []string {
	return h[key]
}
func (h header) Keys() []string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	return keys
}

// grpcTransport is a transport.Transporter of kind grpc.
type grpcTransport struct {
	endpoint  string
	operation string
	reqHeader header
}

func newGrpcTransport(endpoint, operation string) *grpcTransport {
	return &grpcTransport{endpoint: endpoint, operation: operation, reqHeader: header{}}
}

func (t *grpcTransport) Kind() transport.Kind            { return transport.KindGRPC }
func (t *grpcTransport) Endpoint() string                { return t.endpoint }
func (t *grpcTransport) Operation() string               { return t.operation }
func (t *grpcTransport) RequestHeader() transport.Header { return t.reqHeader }
func (t *grpcTransport) ReplyHeader() transport.Header   { return header{} }

// httpTransport is a transport/http.Transporter, the interface the plugin type
// asserts to reach the underlying *http.Request.
type httpTransport struct {
	grpcTransport
	req *http.Request
}

var _ transhttp.Transporter = (*httpTransport)(nil)

func newHttpTransport(endpoint, operation string, req *http.Request) *httpTransport {
	return &httpTransport{
		grpcTransport: grpcTransport{endpoint: endpoint, operation: operation, reqHeader: header{}},
		req:           req,
	}
}

func (t *httpTransport) Kind() transport.Kind { return transport.KindHTTP }
func (t *httpTransport) Request() *http.Request {
	return t.req
}
func (t *httpTransport) PathTemplate() string { return t.operation }

// pinpointHeaders are the distributed tracing headers Inject writes; the callee
// continues the transaction from them.
var pinpointHeaders = []string{
	pinpoint.HeaderTraceId,
	pinpoint.HeaderSpanId,
	pinpoint.HeaderParentSpanId,
	pinpoint.HeaderParentApplicationName,
}

// A kratos endpoint carries its scheme; the span's endpoint must be the bare
// address, or the same server is filed under two names on the server map.
func Test_serverEndpoint(t *testing.T) {
	for _, tt := range []struct{ endpoint, want string }{
		{"grpc://127.0.0.1:9000", "127.0.0.1:9000"},
		{"http://127.0.0.1:8000", "127.0.0.1:8000"},
		{"127.0.0.1:8000", "127.0.0.1:8000"},
		{"", ""},
		{"discovery:///provider", "/provider"},
		{"//127.0.0.1:8000", "127.0.0.1:8000"},
	} {
		assert.Equal(t, tt.want, serverEndpoint(tt.endpoint), "serverEndpoint(%q)", tt.endpoint)
	}
}

// The remote address recorded on a server span comes from a different place per
// transport, and anything that is not host:port has to fall back rather than
// record a port or an empty string.
func Test_serverRemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	req.RemoteAddr = "10.0.0.1:54321"
	httpCtx := transport.NewServerContext(context.Background(),
		newHttpTransport("http://127.0.0.1:8000", "/helloworld.Greeter/SayHello", req))
	assert.Equal(t, "10.0.0.1", serverRemoteAddr(httpCtx), "an HTTP peer address is stripped of its port")

	grpcCtx := transport.NewServerContext(context.Background(),
		newGrpcTransport("grpc://127.0.0.1:9000", "/helloworld.Greeter/SayHello"))
	grpcCtx = peer.NewContext(grpcCtx, &peer.Peer{Addr: &net.TCPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 54321}})
	assert.Equal(t, "10.0.0.2", serverRemoteAddr(grpcCtx), "a gRPC peer address comes off the peer")

	// A gRPC call without a peer, and an HTTP request without a remote address,
	// both leave nothing to split.
	bare := transport.NewServerContext(context.Background(),
		newGrpcTransport("grpc://127.0.0.1:9000", "/helloworld.Greeter/SayHello"))
	assert.Equal(t, "127.0.0.1", serverRemoteAddr(bare), "a gRPC call with no peer falls back")

	noAddr := httptest.NewRequest(http.MethodGet, "/hello", nil)
	noAddr.RemoteAddr = ""
	assert.Equal(t, "127.0.0.1", serverRemoteAddr(transport.NewServerContext(context.Background(),
		newHttpTransport("http://127.0.0.1:8000", "/hello", noAddr))),
		"an HTTP request with no peer address falls back")

	// An IPv6 peer address still splits into a bare host.
	v6 := httptest.NewRequest(http.MethodGet, "/hello", nil)
	v6.RemoteAddr = "[2001:db8::1]:54321"
	assert.Equal(t, "2001:db8::1", serverRemoteAddr(transport.NewServerContext(context.Background(),
		newHttpTransport("http://127.0.0.1:8000", "/hello", v6))))
}

// The destination recorded for a client call is the callee: the Host header for
// HTTP, the dial endpoint for gRPC.
func Test_clientRemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://provider:8000/hello", nil)
	assert.Equal(t, "provider:8000", clientRemoteAddr(newHttpTransport("", "/helloworld.Greeter/SayHello", req)))

	assert.Equal(t, "discovery:///provider",
		clientRemoteAddr(newGrpcTransport("discovery:///provider", "/helloworld.Greeter/SayHello")))

	// A gRPC transport with no endpoint leaves nothing to record.
	assert.Equal(t, "127.0.0.1", clientRemoteAddr(newGrpcTransport("", "/helloworld.Greeter/SayHello")))
}

func Test_makeUrl(t *testing.T) {
	httpTr := newHttpTransport("", "/helloworld.Greeter/SayHello", httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, "http://provider:8000/helloworld.Greeter/SayHello", makeUrl(httpTr, "provider:8000"))

	grpcTr := newGrpcTransport("", "/helloworld.Greeter/SayHello")
	assert.Equal(t, "grpc://provider:9000/helloworld.Greeter/SayHello", makeUrl(grpcTr, "provider:9000"))
}

// The middleware wraps the handler, so the handler's reply and error have to
// come back untouched, with a sampled tracer in its context.
func TestServerMiddleware(t *testing.T) {
	startAgent(t)

	for _, tt := range []struct {
		name string
		tr   transport.Transporter
	}{
		{"http", newHttpTransport("http://127.0.0.1:8000", "/helloworld.Greeter/SayHello",
			httptest.NewRequest(http.MethodGet, "/hello", nil))},
		{"grpc", newGrpcTransport("grpc://127.0.0.1:9000", "/helloworld.Greeter/SayHello")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			want := errors.New("handler failed")
			var tracer pinpoint.Tracer

			reply, err := ServerMiddleware()(func(ctx context.Context, req interface{}) (interface{}, error) {
				tracer = pinpoint.FromContext(ctx)
				return "reply", want
			})(transport.NewServerContext(context.Background(), tt.tr), "request")

			require.NotNil(t, tracer)
			assert.True(t, tracer.IsSampled(), "handler received an unsampled tracer")
			assert.Equal(t, "reply", reply, "the handler's reply must come back unchanged")
			assert.ErrorIs(t, err, want, "the handler's error must come back unchanged")

			span := spanOf(t, tracer)
			assert.Equal(t, "/helloworld.Greeter/SayHello", span["RpcName"],
				"the span is named after the kratos operation")
			assert.Equal(t, "127.0.0.1:"+map[string]string{"http": "8000", "grpc": "9000"}[tt.name], span["EndPoint"],
				"the span's endpoint must be the bare address, without the scheme")
			assert.NotEqual(t, float64(0), span["Err"], "the handler error must fail the span")
		})
	}
}

// A handler that succeeds must leave the span unfailed.
func TestServerMiddleware_SuccessfulHandler(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	reply, err := ServerMiddleware()(func(ctx context.Context, req interface{}) (interface{}, error) {
		tracer = pinpoint.FromContext(ctx)
		return "reply", nil
	})(transport.NewServerContext(context.Background(),
		newGrpcTransport("grpc://127.0.0.1:9000", "/helloworld.Greeter/SayHello")), "request")

	require.NoError(t, err)
	assert.Equal(t, "reply", reply)
	assert.Equal(t, float64(0), spanOf(t, tracer)["Err"], "a successful handler must not fail the span")
}

// A kratos server is usually one hop of a larger call: the tracing headers the
// caller sent arrive in the request header and have to put this span in the
// caller's transaction.
func TestServerMiddleware_ContinuesTheCallersTransaction(t *testing.T) {
	startAgent(t)

	caller := pinpoint.GetAgent().NewSpanTracer("caller", "/caller")
	defer caller.EndSpan()

	tr := newGrpcTransport("grpc://127.0.0.1:9000", "/helloworld.Greeter/SayHello")
	caller.NewSpanEvent("call")
	caller.Inject(tr.reqHeader)
	caller.EndSpanEvent()

	var tracer pinpoint.Tracer
	_, err := ServerMiddleware()(func(ctx context.Context, req interface{}) (interface{}, error) {
		tracer = pinpoint.FromContext(ctx)
		return nil, nil
	})(transport.NewServerContext(context.Background(), tr), "request")

	require.NoError(t, err)
	require.NotNil(t, tracer)
	assert.Equal(t, caller.TransactionId().String(), tracer.TransactionId().String())
}

// A handler reached without a kratos server transport - a plain call, or a
// transport kind the middleware was not mounted on - must still run.
func TestServerMiddleware_WithoutAServerTransport(t *testing.T) {
	startAgent(t)

	called := false
	_, err := ServerMiddleware()(func(ctx context.Context, req interface{}) (interface{}, error) {
		called = true
		assert.False(t, pinpoint.FromContext(ctx).IsSampled(),
			"a context with no server transport produced a sampled tracer")
		return nil, nil
	})(context.Background(), "request")

	require.NoError(t, err)
	assert.True(t, called, "the handler did not run")
}

// A panicking handler must not be swallowed by the middleware.
func TestServerMiddleware_PanicPropagates(t *testing.T) {
	startAgent(t)

	ctx := transport.NewServerContext(context.Background(),
		newGrpcTransport("grpc://127.0.0.1:9000", "/helloworld.Greeter/SayHello"))

	assert.PanicsWithValue(t, "boom", func() {
		_, _ = ServerMiddleware()(func(context.Context, interface{}) (interface{}, error) {
			panic("boom")
		})(ctx, "request")
	})
}

// With no agent running the middleware must be a straight pass-through.
func TestServerMiddleware_PassesThroughWhenAgentDisabled(t *testing.T) {
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

	ctx := transport.NewServerContext(context.Background(),
		newGrpcTransport("grpc://127.0.0.1:9000", "/helloworld.Greeter/SayHello"))

	called := false
	_, err := ServerMiddleware()(func(ctx context.Context, req interface{}) (interface{}, error) {
		called = true
		assert.False(t, pinpoint.FromContext(ctx).IsSampled(), "a disabled agent produced a sampled tracer")
		return nil, nil
	})(ctx, "request")

	require.NoError(t, err)
	assert.True(t, called, "the handler did not run")
}

// The client middleware is what links the caller's span to the callee's, so it
// has to inject the distributed-tracing headers into the outgoing request
// header before the call, and return the call's result unchanged.
func TestClientMiddleware_InjectsTracingHeaders(t *testing.T) {
	startAgent(t)

	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/caller")
	defer tracer.EndSpan()

	tr := newGrpcTransport("provider:9000", "/helloworld.Greeter/SayHello")
	ctx := transport.NewClientContext(pinpoint.NewContext(context.Background(), tracer), tr)

	want := errors.New("rpc failed")
	reply, err := ClientMiddleware()(func(context.Context, interface{}) (interface{}, error) {
		return "reply", want
	})(ctx, "request")

	assert.Equal(t, "reply", reply, "the call's reply must come back unchanged")
	assert.ErrorIs(t, err, want, "the call's error must come back unchanged")
	for _, key := range pinpointHeaders {
		assert.NotEmpty(t, tr.reqHeader.Get(key), "outgoing request header is missing %s", key)
	}
	assert.Equal(t, tracer.TransactionId().String(), tr.reqHeader.Get(pinpoint.HeaderTraceId))
}

// The callee reads those headers back through the server middleware and has to
// land in the caller's transaction.
func TestClientAndServerShareOneTransaction(t *testing.T) {
	startAgent(t)

	caller := pinpoint.GetAgent().NewSpanTracer("caller", "/caller")
	defer caller.EndSpan()

	tr := newGrpcTransport("provider:9000", "/helloworld.Greeter/SayHello")
	var callee pinpoint.Tracer

	_, err := ClientMiddleware()(func(context.Context, interface{}) (interface{}, error) {
		// Stand in for the callee: it reads the headers the client just wrote.
		_, err := ServerMiddleware()(func(ctx context.Context, req interface{}) (interface{}, error) {
			callee = pinpoint.FromContext(ctx)
			return "reply", nil
		})(transport.NewServerContext(context.Background(), tr), "request")
		return "reply", err
	})(transport.NewClientContext(pinpoint.NewContext(context.Background(), caller), tr), "request")

	require.NoError(t, err)
	require.NotNil(t, callee)
	assert.Equal(t, caller.TransactionId().String(), callee.TransactionId().String())
}

// An HTTP client call records a different service type and url scheme than a
// gRPC one; both have to go through without disturbing the call.
func TestClientMiddleware_HttpTransport(t *testing.T) {
	startAgent(t)

	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/caller")
	defer tracer.EndSpan()

	tr := newHttpTransport("", "/helloworld.Greeter/SayHello",
		httptest.NewRequest(http.MethodGet, "http://provider:8000/hello", nil))
	ctx := transport.NewClientContext(pinpoint.NewContext(context.Background(), tracer), tr)

	reply, err := ClientMiddleware()(func(context.Context, interface{}) (interface{}, error) {
		return "reply", nil
	})(ctx, "request")

	require.NoError(t, err)
	assert.Equal(t, "reply", reply)
	for _, key := range pinpointHeaders {
		assert.NotEmpty(t, tr.reqHeader.Get(key), "outgoing request header is missing %s", key)
	}
}

// A call made without a kratos client transport must still go through.
func TestClientMiddleware_WithoutAClientTransport(t *testing.T) {
	startAgent(t)

	called := false
	_, err := ClientMiddleware()(func(context.Context, interface{}) (interface{}, error) {
		called = true
		return nil, nil
	})(context.Background(), "request")

	require.NoError(t, err)
	assert.True(t, called, "the call did not go through")
}

// A caller whose context never had a span hands the middleware a noop tracer;
// it must record nothing and still make the call.
func TestClientMiddleware_WithNoopTracer(t *testing.T) {
	startAgent(t)

	tr := newGrpcTransport("provider:9000", "/helloworld.Greeter/SayHello")
	ctx := transport.NewClientContext(context.Background(), tr)

	called := false
	_, err := ClientMiddleware()(func(context.Context, interface{}) (interface{}, error) {
		called = true
		return nil, nil
	})(ctx, "request")

	require.NoError(t, err)
	assert.True(t, called, "the call did not go through")
	assert.Equal(t, "s0", tr.reqHeader.Get(pinpoint.HeaderSampled),
		"an untraced call must tell the callee not to trace either")
}
