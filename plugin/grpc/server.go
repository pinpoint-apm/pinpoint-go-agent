// Package ppgrpc instruments the grpc/grpc-go package (https://github.com/grpc/grpc-go).
//
// This package instruments gRPC servers and clients.
// To instrument a gRPC server, use UnaryServerInterceptor and StreamServerInterceptor.
//
//	grpcServer := grpc.NewServer(
//	    grpc.UnaryInterceptor(ppgrpc.UnaryServerInterceptor()),
//	    grpc.StreamInterceptor(ppgrpc.StreamServerInterceptor()),
//	)
//
// ppgrpc's server interceptor adds the pinpoint.Tracer to the gRPC server handler's context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
//
//	func (s *Server) UnaryCallUnaryReturn(ctx context.Context, msg *testapp.Greeting) (*testapp.Greeting, error) {
//	    tracer := pinpoint.FromContext(ctx)
//	    tracer.NewSpanEvent("gRPC Server Handler").EndSpanEvent()
//	    return "hi", nil
//	}
//
// To instrument a gRPC client, use UnaryClientInterceptor and StreamClientInterceptor.
//
//	conn, err := grpc.NewClient(
//	    "localhost:8080",
//	    grpc.WithUnaryInterceptor(ppgrpc.UnaryClientInterceptor()),
//	    grpc.WithStreamInterceptor(ppgrpc.StreamClientInterceptor()),
//	)
//
// It is necessary to pass the context containing the pinpoint.Tracer to grpc.Client.
//
//	client := testapp.NewHelloClient(conn)
//	client.UnaryCallUnaryReturn(pinpoint.NewContext(context.Background(), tracer), greeting)
package ppgrpc

import (
	"context"
	"net"
	"strings"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

type serverStream struct {
	grpc.ServerStream
	context context.Context
}

func (s *serverStream) Context() context.Context {
	return s.context
}

// distributedTracingContextReaderMD reads header values straight off the
// incoming context: metadata.FromIncomingContext copies the caller's whole MD
// - a map, a lowered key string and a value slice per header - only to serve
// the ten point lookups tracing needs.
type distributedTracingContextReaderMD struct {
	ctx context.Context
}

func (m distributedTracingContextReaderMD) Get(key string) string {
	if v := metadata.ValueFromIncomingContext(m.ctx, loweredKey(key)); len(v) > 0 {
		return v[0]
	}
	return ""
}

// loweredHeaderKeys pre-lowers the pinpoint header names once: metadata keys
// are stored lowercase, so a lowered key takes ValueFromIncomingContext's
// direct map hit, where a mixed-case one costs an allocation or a scan of the
// whole MD per lookup.
var loweredHeaderKeys = func() map[string]string {
	m := make(map[string]string)
	for _, k := range []string{
		pinpoint.HeaderTraceId, pinpoint.HeaderSpanId, pinpoint.HeaderParentSpanId,
		pinpoint.HeaderSampled, pinpoint.HeaderFlags, pinpoint.HeaderParentApplicationName,
		pinpoint.HeaderParentApplicationType, pinpoint.HeaderParentApplicationNamespace,
		pinpoint.HeaderParentServiceName, pinpoint.HeaderHost,
	} {
		m[k] = strings.ToLower(k)
	}
	return m
}()

func loweredKey(key string) string {
	if l, ok := loweredHeaderKeys[key]; ok {
		return l
	}
	return strings.ToLower(key)
}

func startSpan(ctx context.Context, rpcName string) pinpoint.Tracer {
	reader := distributedTracingContextReaderMD{ctx}
	tracer := pinpoint.GetAgent().NewSpanTracerWithReader("gRPC Server", rpcName, reader)
	tracer.Span().SetServiceType(pinpoint.ServiceTypeGrpcServer)
	// remoteAddr formats the peer address; an unsampled span would discard it.
	if tracer.IsSampled() {
		tracer.Span().SetRemoteAddress(remoteAddr(ctx))
	}

	return tracer
}

func remoteAddr(ctx context.Context) (addr string) {
	addr = ""
	if p, ok := peer.FromContext(ctx); ok {
		addr = p.Addr.String()
	}
	if addr, _, _ = net.SplitHostPort(addr); addr == "" {
		addr = "127.0.0.1"
	}
	return addr
}

// UnaryServerInterceptor returns a grpc.UnaryServerInterceptor ready to instrument
// and adds the pinpoint.Tracer to the grpc.UnaryHandler's context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if !pinpoint.GetAgent().Enable() {
			return handler(ctx, req)
		}

		tracer := startSpan(ctx, info.FullMethod)
		defer tracer.EndSpan()
		defer tracer.NewSpanEvent(info.FullMethod).EndSpanEvent()

		ctx = pinpoint.NewContext(ctx, tracer)
		resp, err := handler(ctx, req)
		tracer.Span().SetError(err)
		return resp, err
	}
}

// StreamServerInterceptor returns a grpc.StreamServerInterceptor ready to instrument
// and adds the pinpoint.Tracer to the grpc.StreamHandler's context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
func StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if !pinpoint.GetAgent().Enable() {
			return handler(srv, stream)
		}

		tracer := startSpan(stream.Context(), info.FullMethod)
		defer tracer.EndSpan()
		defer tracer.NewSpanEvent(info.FullMethod).EndSpanEvent()

		ctx := pinpoint.NewContext(stream.Context(), tracer)
		wrappedStream := &serverStream{stream, ctx}
		err := handler(srv, wrappedStream)
		tracer.Span().SetError(err)
		return err
	}
}
