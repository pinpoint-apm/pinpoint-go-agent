// Package ppkratos instruments the go-kratos/kratos/v2 package (https://github.com/go-kratos/kratos).
//
// This package instruments kratos servers and clients.
// To instrument a kratos server, use ppkratos.ServerMiddleware.
//
//   httpSrv := http.NewServer(
//     http.Address(":8000"),
//     http.Middleware(ppkratos.ServerMiddleware()),
//   )
//
// To instrument a gRPC client, use ppkratos.ClientMiddleware.
//
//   conn, err := transhttp.NewClient(
//     context.Background(),
//     transhttp.WithMiddleware(ppkratos.ClientMiddleware()),
//     transhttp.WithEndpoint("127.0.0.1:8000"),
//   )
//
package ppkratos

import (
	"bytes"
	"context"
	"net"
	"strings"

	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/transport"
	"github.com/go-kratos/kratos/v2/transport/http"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"google.golang.org/grpc/peer"
)

func ServerMiddleware() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (reply interface{}, err error) {
			if !pinpoint.GetAgent().Enable() {
				return handler(ctx, req)
			}

			if tr, ok := transport.FromServerContext(ctx); ok {
				tracer := pinpoint.GetAgent().NewSpanTracerWithReader("Kratos Server", tr.Operation(), tr.RequestHeader())
				defer tracer.EndSpan()
				defer tracer.NewSpanEvent(tr.Operation()).EndSpanEvent()

				span := tracer.Span()
				if tr.Kind() == transport.KindGRPC {
					span.SetServiceType(pinpoint.ServiceTypeGrpcServer)
				}
				span.SetEndPoint(serverEndpoint(tr.Endpoint()))
				span.SetRemoteAddress(serverRemoteAddr(ctx))

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

func serverRemoteAddr(ctx context.Context) (addr string) {
	tr, _ := transport.FromServerContext(ctx)

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
	if addr, _, _ = net.SplitHostPort(addr); addr == "" {
		addr = "127.0.0.1"
	}
	return addr
}

func serverEndpoint(endpoint string) string {
	if i := strings.Index(endpoint, "//"); i > -1 {
		return endpoint[i+2:]
	} else {
		return endpoint
	}
}

func ClientMiddleware() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (reply interface{}, err error) {
			if tr, ok := transport.FromClientContext(ctx); ok {
				tracer := pinpoint.FromContext(ctx)
				defer tracer.NewSpanEvent(tr.Operation()).EndSpanEvent()

				se := tracer.SpanEvent()
				if tr.Kind() == transport.KindGRPC {
					se.SetServiceType(pinpoint.ServiceTypeGrpc)
				} else {
					se.SetServiceType(pinpoint.ServiceTypeGoHttpClient)
				}

				remote := clientRemoteAddr(tr)
				se.SetDestination(remote)
				se.Annotations().AppendString(pinpoint.AnnotationHttpUrl, makeUrl(tr, remote))

				tracer.Inject(tr.RequestHeader())
				reply, err = handler(ctx, req)
				se.SetError(err, "rpc error")
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
	var buf bytes.Buffer
	switch tr.Kind() {
	case transport.KindHTTP:
		buf.WriteString("http://")
	case transport.KindGRPC:
		buf.WriteString("grpc://")
	}
	buf.WriteString(addr)
	buf.WriteString(tr.Operation())
	return buf.String()
}
