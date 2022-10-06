package ppgrpc

import (
	"context"
	"google.golang.org/grpc/peer"
	"io"
	"net"
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
		endSpan(cs.tracer, err)
		cs.isFinished = true
	}
}

type distributedTracingContextWriterMD struct {
	md metadata.MD
}

func (m *distributedTracingContextWriterMD) Set(key string, value string) {
	m.md.Set(key, value)
}

func newSpanForGrpcClient(ctx context.Context, method string) (context.Context, pinpoint.Tracer) {
	tracer := pinpoint.FromContext(ctx)
	if tracer == nil {
		return ctx, nil
	}

	tracer = tracer.NewSpanEvent(method)
	tracer.SpanEvent().SetServiceType(pinpoint.ServiceTypeGrpc)

	var p peer.Peer
	grpc.Peer(&p)
	var remoteAddress string
	if p.Addr != nil {
		remoteAddress, _, _ = net.SplitHostPort(p.Addr.String())
	} else {
		remoteAddress = "localhost"
	}

	url := "http://" + remoteAddress + "/" + method
	tracer.SpanEvent().Annotations().AppendString(pinpoint.AnnotationHttpUrl, url)
	tracer.SpanEvent().SetDestination(remoteAddress)

	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		md = metadata.New(nil)
	}

	writer := &distributedTracingContextWriterMD{md}
	tracer.Inject(writer)
	ctx = metadata.NewOutgoingContext(ctx, writer.md)

	return ctx, tracer
}

func endSpan(tracer pinpoint.Tracer, err error) {
	if tracer != nil {
		if err != nil && err != io.EOF {
			tracer.SpanEvent().SetError(err, "grpc error")
		}
		tracer.EndSpanEvent()
	}
}

// UnaryClientInterceptor returns a new grpc.UnaryClientInterceptor ready to instrument.
func UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		newCtx, clientSpan := newSpanForGrpcClient(ctx, method)
		err := invoker(newCtx, method, req, reply, cc, opts...)
		endSpan(clientSpan, err)
		return err
	}
}

// StreamClientInterceptor returns a new grpc.StreamClientInterceptor ready to instrument.
func StreamClientInterceptor() grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		newCtx, span := newSpanForGrpcClient(ctx, method)
		stream, err := streamer(newCtx, desc, cc, method, opts...)
		if err != nil {
			endSpan(span, err)
			return nil, err
		}
		return &clientStream{ClientStream: stream, tracer: span}, nil
	}
}
