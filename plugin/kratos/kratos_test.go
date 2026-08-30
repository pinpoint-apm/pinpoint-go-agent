package ppkratos

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-kratos/kratos/v2/transport"
	transhttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/pinpoint-apm/pinpoint-go-agent"
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

// A kratos endpoint carries its scheme; the span's endpoint must be the bare
// address, or the same server is filed under two names on the server map.
func Test_serverEndpoint(t *testing.T) {
	for _, tt := range []struct{ endpoint, want string }{
		{"grpc://127.0.0.1:9000", "127.0.0.1:9000"},
		{"http://127.0.0.1:8000", "127.0.0.1:8000"},
		{"127.0.0.1:8000", "127.0.0.1:8000"},
		{"", ""},
	} {
		if got := serverEndpoint(tt.endpoint); got != tt.want {
			t.Errorf("serverEndpoint(%q) = %q, want %q", tt.endpoint, got, tt.want)
		}
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
	if got := serverRemoteAddr(httpCtx); got != "10.0.0.1" {
		t.Errorf("serverRemoteAddr(http) = %q, want 10.0.0.1", got)
	}

	grpcCtx := transport.NewServerContext(context.Background(),
		newGrpcTransport("grpc://127.0.0.1:9000", "/helloworld.Greeter/SayHello"))
	grpcCtx = peer.NewContext(grpcCtx, &peer.Peer{Addr: &net.TCPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 54321}})
	if got := serverRemoteAddr(grpcCtx); got != "10.0.0.2" {
		t.Errorf("serverRemoteAddr(grpc) = %q, want 10.0.0.2", got)
	}

	// A gRPC call without a peer, and an HTTP request without a remote address,
	// both leave nothing to split.
	bare := transport.NewServerContext(context.Background(),
		newGrpcTransport("grpc://127.0.0.1:9000", "/helloworld.Greeter/SayHello"))
	if got := serverRemoteAddr(bare); got != "127.0.0.1" {
		t.Errorf("serverRemoteAddr(no peer) = %q, want 127.0.0.1", got)
	}
}

// The destination recorded for a client call is the callee: the Host header for
// HTTP, the dial endpoint for gRPC.
func Test_clientRemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://provider:8000/hello", nil)
	if got := clientRemoteAddr(newHttpTransport("", "/helloworld.Greeter/SayHello", req)); got != "provider:8000" {
		t.Errorf("clientRemoteAddr(http) = %q, want provider:8000", got)
	}

	if got := clientRemoteAddr(newGrpcTransport("discovery:///provider", "/helloworld.Greeter/SayHello")); got != "discovery:///provider" {
		t.Errorf("clientRemoteAddr(grpc) = %q, want discovery:///provider", got)
	}

	// A gRPC transport with no endpoint leaves nothing to record.
	if got := clientRemoteAddr(newGrpcTransport("", "/helloworld.Greeter/SayHello")); got != "127.0.0.1" {
		t.Errorf("clientRemoteAddr(empty) = %q, want 127.0.0.1", got)
	}
}

func Test_makeUrl(t *testing.T) {
	httpTr := newHttpTransport("", "/helloworld.Greeter/SayHello", httptest.NewRequest(http.MethodGet, "/", nil))
	if got, want := makeUrl(httpTr, "provider:8000"), "http://provider:8000/helloworld.Greeter/SayHello"; got != want {
		t.Errorf("makeUrl(http) = %q, want %q", got, want)
	}

	grpcTr := newGrpcTransport("", "/helloworld.Greeter/SayHello")
	if got, want := makeUrl(grpcTr, "provider:9000"), "grpc://provider:9000/helloworld.Greeter/SayHello"; got != want {
		t.Errorf("makeUrl(grpc) = %q, want %q", got, want)
	}
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
			var sampled bool

			reply, err := ServerMiddleware()(func(ctx context.Context, req interface{}) (interface{}, error) {
				sampled = pinpoint.FromContext(ctx).IsSampled()
				return "reply", want
			})(transport.NewServerContext(context.Background(), tt.tr), "request")

			if !sampled {
				t.Error("handler received an unsampled tracer")
			}
			if reply != "reply" {
				t.Errorf("reply = %v, want %q", reply, "reply")
			}
			if !errors.Is(err, want) {
				t.Errorf("error = %v, want %v", err, want)
			}
		})
	}
}

// A handler reached without a kratos server transport - a plain call, or a
// transport kind the middleware was not mounted on - must still run.
func TestServerMiddleware_WithoutAServerTransport(t *testing.T) {
	startAgent(t)

	called := false
	if _, err := ServerMiddleware()(func(ctx context.Context, req interface{}) (interface{}, error) {
		called = true
		if pinpoint.FromContext(ctx).IsSampled() {
			t.Error("a context with no server transport produced a sampled tracer")
		}
		return nil, nil
	})(context.Background(), "request"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("the handler did not run")
	}
}

// With no agent running the middleware must be a straight pass-through.
func TestServerMiddleware_PassesThroughWhenAgentDisabled(t *testing.T) {
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

	ctx := transport.NewServerContext(context.Background(),
		newGrpcTransport("grpc://127.0.0.1:9000", "/helloworld.Greeter/SayHello"))

	called := false
	if _, err := ServerMiddleware()(func(ctx context.Context, req interface{}) (interface{}, error) {
		called = true
		if pinpoint.FromContext(ctx).IsSampled() {
			t.Error("a disabled agent produced a sampled tracer")
		}
		return nil, nil
	})(ctx, "request"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("the handler did not run")
	}
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

	if reply != "reply" {
		t.Errorf("reply = %v, want %q", reply, "reply")
	}
	if !errors.Is(err, want) {
		t.Errorf("error = %v, want %v", err, want)
	}
	for _, key := range []string{
		pinpoint.HeaderTraceId,
		pinpoint.HeaderSpanId,
		pinpoint.HeaderParentSpanId,
		pinpoint.HeaderParentApplicationName,
	} {
		if tr.reqHeader.Get(key) == "" {
			t.Errorf("outgoing request header is missing %s", key)
		}
	}
}

// A call made without a kratos client transport must still go through.
func TestClientMiddleware_WithoutAClientTransport(t *testing.T) {
	startAgent(t)

	called := false
	if _, err := ClientMiddleware()(func(context.Context, interface{}) (interface{}, error) {
		called = true
		return nil, nil
	})(context.Background(), "request"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("the call did not go through")
	}
}
