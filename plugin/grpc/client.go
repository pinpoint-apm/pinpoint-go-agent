package ppgrpc

import (
	"bytes"
	"context"
	"io"
	"strings"
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
		endSpanEvent(cs.tracer, err)
		cs.isFinished = true
	}
}

type distributedTracingContextWriterMD struct {
	md metadata.MD
}

func (m *distributedTracingContextWriterMD) Set(key string, value string) {
	m.md.Set(key, value)
}

func newClientTracer(ctx context.Context, method string, target string) (context.Context, pinpoint.Tracer) {
	tracer := pinpoint.FromContext(ctx).NewSpanEvent(method)
	if tracer.IsSampled() {
		se := tracer.SpanEvent()
		se.SetServiceType(pinpoint.ServiceTypeGrpc)

		//refer https://github.com/grpc/grpc/blob/master/doc/naming.md
		var remote string
		if strings.HasPrefix(target, "unix:") {
			remote = "localhost"
		} else {
			remote = strings.TrimPrefix(target, "dns:///")
		}
		se.SetDestination(remote)
		se.Annotations().AppendString(pinpoint.AnnotationHttpUrl, makeUrl(remote, method))
	}

	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		md = metadata.New(nil)
	}
	writer := &distributedTracingContextWriterMD{md}
	tracer.Inject(writer)
	ctx = metadata.NewOutgoingContext(ctx, writer.md)

	return ctx, tracer
}

func makeUrl(remote string, method string) string {
	var buf bytes.Buffer
	buf.WriteString("grpc://")
	buf.WriteString(remote)
	buf.WriteString(method)
	return buf.String()
}

func endSpanEvent(tracer pinpoint.Tracer, err error) {
	defer tracer.EndSpanEvent()
	if err != nil && err != io.EOF {
		tracer.SpanEvent().SetError(err, "grpc error")
	}
}

// UnaryClientInterceptor returns a new grpc.UnaryClientInterceptor ready to instrument.
func UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		newCtx, tracer := newClientTracer(ctx, method, cc.Target())
		err := invoker(newCtx, method, req, reply, cc, opts...)
		endSpanEvent(tracer, err)
		return err
	}
}

// StreamClientInterceptor returns a new grpc.StreamClientInterceptor ready to instrument.
func StreamClientInterceptor() grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		newCtx, tracer := newClientTracer(ctx, method, cc.Target())
		stream, err := streamer(newCtx, desc, cc, method, opts...)
		if err != nil {
			endSpanEvent(tracer, err)
			return nil, err
		}
		return &clientStream{ClientStream: stream, tracer: tracer}, nil
	}
}
