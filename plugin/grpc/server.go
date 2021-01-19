package grpc

import (
	"context"
	"strings"

	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const serviceTypeGrpcServer = 1130

type serverStream struct {
	grpc.ServerStream
	context context.Context
}

func (s *serverStream) Context() context.Context {
	return s.context
}

type DistributedTracingContextReaderMD struct {
	md metadata.MD
}

func (m DistributedTracingContextReaderMD) Get(key string) string {
	v := m.md.Get(key)
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

func startSpan(ctx context.Context, agent *pinpoint.Agent, apiId int32) pinpoint.Tracer {
	md, _ := metadata.FromIncomingContext(ctx) // nil is ok
	reader := &DistributedTracingContextReaderMD{md}
	tracer := agent.NewSpanTracerWithReader("Go GRPC Server", reader)
	tracer.Span().SetServiceType(serviceTypeGrpcServer)
	tracer.Span().SetApiId(apiId)

	return tracer
}

func UnaryServerInterceptor(agent *pinpoint.Agent) grpc.UnaryServerInterceptor {
	apiId := agent.RegisterSpanApiId("Go gRPC HTTP Server", pinpoint.ApiTypeWebRequest)

	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if agent == nil {
			return handler(ctx, req)
		}

		tracer := startSpan(ctx, agent, apiId)
		defer tracer.EndSpan()

		method := strings.TrimPrefix(info.FullMethod, "/")
		defer tracer.NewSpanEvent(method).EndSpanEvent()

		ctx = pinpoint.NewContext(ctx, tracer)
		resp, err := handler(ctx, req)
		tracer.Span().SetError(err)
		return resp, err
	}
}

func StreamServerInterceptor(agent *pinpoint.Agent) grpc.StreamServerInterceptor {
	apiId := agent.RegisterSpanApiId("Go gRPC HTTP Server", pinpoint.ApiTypeWebRequest)

	return func(srv interface{}, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if agent == nil {
			return handler(srv, stream)
		}

		tracer := startSpan(stream.Context(), agent, apiId)
		defer tracer.EndSpan()

		method := strings.TrimPrefix(info.FullMethod, "/")
		defer tracer.NewSpanEvent(method).EndSpanEvent()

		ctx := pinpoint.NewContext(stream.Context(), tracer)
		wrappedStream := &serverStream{stream, ctx}
		err := handler(srv, wrappedStream)
		tracer.Span().SetError(err)
		return err
	}
}
