// Package ppkratosv3 instruments the go-kratos/kratos/v3 package (https://github.com/go-kratos/kratos).
//
// This package instruments kratos servers and clients.
// To instrument a kratos server, use ServerMiddleware.
//
//	httpSrv := http.NewServer(
//		http.Address(":8000"),
//		http.Middleware(ppkratosv3.ServerMiddleware()),
//	)
//
// The server middleware adds the pinpoint.Tracer to the kratos server handler's context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
//
//	func (s *server) SayHello(ctx context.Context, in *helloworld.HelloRequest) (*helloworld.HelloReply, error) {
//		tracer := pinpoint.FromContext(ctx)
//		defer tracer.NewSpanEvent("f1").EndSpanEvent()
//		...
//	}
//
// To instrument a kratos client, use ClientMiddleware.
//
//	conn, err := transhttp.NewClient(
//		context.Background(),
//		transhttp.WithMiddleware(ppkratosv3.ClientMiddleware()),
//		transhttp.WithEndpoint("127.0.0.1:8000"),
//	)
//
// It is necessary to pass the context containing the pinpoint.Tracer to kratos client.
//
//	client := pb.NewGreeterHTTPClient(conn)
//	reply, err := client.SayHello(pinpoint.NewContext(context.Background(), tracer), &pb.HelloRequest{Name: "kratos"})
package ppkratosv3

import (
	"context"
	"net/textproto"
	"strings"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
	"github.com/go-kratos/kratos/v3/transport/http"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"google.golang.org/grpc/peer"
)

// ServerMiddleware returns a server side middleware ready to instrument
// and adds the pinpoint.Tracer to the handler's context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
func ServerMiddleware() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (reply interface{}, err error) {
			if !pinpoint.GetAgent().Enable() {
				return handler(ctx, req)
			}

			if tr, ok := transport.FromServerContext(ctx); ok {
				tracer := serverTracer(ctx, tr)
				defer tracer.EndSpan()
				defer tracer.NewSpanEvent(tr.Operation()).EndSpanEvent()

				span := tracer.Span()
				// A nested layer leaves the span-level request attributes to
				// the layer that owns the span.
				if !pinpoint.IsNestedTracer(tracer) {
					if tr.Kind() == transport.KindGRPC {
						span.SetServiceType(pinpoint.ServiceTypeGrpcServer)
					}
					// The endpoint trim and peer-address formatting are
					// discarded on an unsampled span.
					if tracer.IsSampled() {
						span.SetEndPoint(serverEndpoint(tr.Endpoint()))
						span.SetRemoteAddress(serverRemoteAddr(ctx, tr))
					}
				}

				ctx = pinpoint.NewContext(ctx, tracer)
				reply, err = handler(ctx, req)
				span.SetError(err)
				return reply, err
			} else {
				return handler(ctx, req)
			}
		}
	}
}

// serverTracer starts the request's span, or hands back a nested view of the
// tracer the context already carries (pinpoint.NestedTracer) - the middleware
// registered twice, or a gRPC server that also runs ppgrpc's interceptor - so
// one request makes one span, as ppgrpc and pphttp do: this layer's event goes
// on the existing span and its EndSpan is ignored.
func serverTracer(ctx context.Context, tr transport.Transporter) pinpoint.Tracer {
	if existing := pinpoint.FromContext(ctx); existing != pinpoint.NoopTracer() {
		return pinpoint.NestedTracer(existing)
	}
	return pinpoint.GetAgent().NewSpanTracerWithReader("Kratos Server", tr.Operation(),
		headerReader{tr.RequestHeader(), tr.Kind()})
}

// serverRemoteAddr takes the transporter the caller already has instead of
// walking the context for it a second time.
func serverRemoteAddr(ctx context.Context, tr transport.Transporter) (addr string) {
	switch tr.Kind() {
	case transport.KindHTTP:
		if ht, ok := tr.(http.Transporter); ok {
			addr = ht.Request().RemoteAddr
		}
	case transport.KindGRPC:
		if p, ok := peer.FromContext(ctx); ok {
			addr = p.Addr.String()
		}
	}
	return pinpoint.RemoteHost(addr)
}

func serverEndpoint(endpoint string) string {
	if _, after, ok := strings.Cut(endpoint, "//"); ok {
		return after
	}
	return endpoint
}

// ClientMiddleware returns a client side middleware ready to instrument.
func ClientMiddleware() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (reply interface{}, err error) {
			// A disabled agent traces nothing and injects nothing - not even
			// the unsampled marker.
			if !pinpoint.GetAgent().Enable() {
				return handler(ctx, req)
			}

			if tr, ok := transport.FromClientContext(ctx); ok {
				tracer := pinpoint.FromContext(ctx)
				defer tracer.NewSpanEvent(tr.Operation()).EndSpanEvent()

				// The URL annotation and destination are discarded on an
				// unsampled span event.
				if tracer.IsSampled() {
					se := tracer.SpanEvent()
					if tr.Kind() == transport.KindGRPC {
						se.SetServiceType(pinpoint.ServiceTypeGrpc)
					} else {
						se.SetServiceType(pinpoint.ServiceTypeGoHttpClient)
					}

					remote := clientRemoteAddr(tr)
					se.SetDestination(remote)
					se.Annotations().AppendString(pinpoint.AnnotationHttpUrl, makeUrl(tr, remote))
				}

				tracer.Inject(headerWriter{tr.RequestHeader(), tr.Kind()})
				reply, err = handler(ctx, req)
				// Guarded: SetError is an interface method, so its variadic
				// name escapes and allocated on every call, nil error and all.
				if err != nil {
					tracer.SpanEvent().SetError(err, "rpc error")
				}
				return reply, err
			} else {
				return handler(ctx, req)
			}
		}
	}
}

func clientRemoteAddr(tr transport.Transporter) (addr string) {
	switch tr.Kind() {
	case transport.KindHTTP:
		if ht, ok := tr.(http.Transporter); ok {
			addr = ht.Request().Host
		}
	case transport.KindGRPC:
		addr = tr.Endpoint()
	}
	if addr == "" {
		addr = "127.0.0.1"
	}
	return addr
}

func makeUrl(tr transport.Transporter, addr string) string {
	scheme := ""
	switch tr.Kind() {
	case transport.KindHTTP:
		scheme = "http://"
	case transport.KindGRPC:
		scheme = "grpc://"
	}
	return scheme + addr + tr.Operation()
}

// The kratos carrier re-spells every key it is given: http.Header canonicalizes
// it and metadata.MD lower-cases it, on Get and Set alike, and each allocates a
// string for seven of the ten pinpoint names, so one request paid some twenty
// allocations reading its trace context and a client call as many writing it.
// Handed the spelling the transport stores, both return the key as it is.
var (
	pinpointHeaderNames = []string{
		pinpoint.HeaderTraceId, pinpoint.HeaderSpanId, pinpoint.HeaderParentSpanId,
		pinpoint.HeaderSampled, pinpoint.HeaderFlags, pinpoint.HeaderParentApplicationName,
		pinpoint.HeaderParentApplicationType, pinpoint.HeaderParentApplicationNamespace,
		pinpoint.HeaderParentServiceName, pinpoint.HeaderHost,
	}
	canonicalKeys = spellHeaderNames(textproto.CanonicalMIMEHeaderKey)
	loweredKeys   = spellHeaderNames(strings.ToLower)
)

func spellHeaderNames(spell func(string) string) map[string]string {
	m := make(map[string]string, len(pinpointHeaderNames))
	for _, k := range pinpointHeaderNames {
		m[k] = spell(k)
	}
	return m
}

// carrierKey is key as the transport of the given kind stores it; another
// kind, or a name outside the pinpoint set, is passed through.
func carrierKey(kind transport.Kind, key string) string {
	var keys map[string]string
	switch kind {
	case transport.KindHTTP:
		keys = canonicalKeys
	case transport.KindGRPC:
		keys = loweredKeys
	default:
		return key
	}
	if k, ok := keys[key]; ok {
		return k
	}
	return key
}

// headerReader adapts a kratos transport.Header to the tracing carrier.
// transport.Header hands out a value and nothing else, so a header carried
// with an empty value cannot be told from one that is absent: it reads as
// absent, and such a request starts a new transaction. That is what this
// plugin did before the carrier reported presence, and only the transport can
// close the gap.
type headerReader struct {
	hdr  transport.Header
	kind transport.Kind
}

func (r headerReader) Get(key string) (string, bool) {
	v := r.hdr.Get(carrierKey(r.kind, key))
	return v, v != ""
}

// headerWriter is Inject's carrier over a kratos transport.Header, spelling
// the keys as headerReader reads them.
type headerWriter struct {
	hdr  transport.Header
	kind transport.Kind
}

func (w headerWriter) Set(key string, value string) {
	w.hdr.Set(carrierKey(w.kind, key), value)
}
