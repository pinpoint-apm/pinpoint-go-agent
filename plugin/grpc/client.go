package grpc

import (
	"context"
	"google.golang.org/grpc/peer"
	"io"
	"net"
	"sync"

	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const serviceTypeGrpc = 9160

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

type DistributedTracingContextWriterMD struct {
	md metadata.MD
}

func (m *DistributedTracingContextWriterMD) Set(key string, value string) {
	m.md.Set(key, value)
}

func newSpanForGrpcClient(ctx context.Context, method string) (context.Context, pinpoint.Tracer) {
	tracer := pinpoint.FromContext(ctx)
	if tracer == nil {
		return ctx, nil
	}

	tracer = tracer.NewSpanEvent(method)
	tracer.SpanEvent().SetServiceType(serviceTypeGrpc)

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

	writer := &DistributedTracingContextWriterMD{md}
	tracer.Inject(writer)
	ctx = metadata.NewOutgoingContext(ctx, writer.md)

	return ctx, tracer
}

func endSpan(tracer pinpoint.Tracer, err error) {
	if err != nil && err != io.EOF {
		tracer.SpanEvent().SetError(err)
	}
	tracer.EndSpanEvent()
}

func UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		newCtx, clientSpan := newSpanForGrpcClient(ctx, method)
		err := invoker(newCtx, method, req, reply, cc, opts...)
		endSpan(clientSpan, err)
		return err
	}
}

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
