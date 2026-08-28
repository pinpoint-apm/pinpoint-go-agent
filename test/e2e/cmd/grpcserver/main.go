// grpcserver is the gRPC downstream of the end-to-end suite. It implements the
// four RPC shapes and echoes the trace context it received back to the caller,
// so propagation is verifiable without reading the collector.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	ppgrpc "github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/example/testapp"
	"github.com/pinpoint-apm/pinpoint-go-agent/test/e2e/internal/e2e"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// traceSuffix encodes the server-side trace context into the reply message.
// testapp.Greeting only carries a message field, so the suite's propagation
// assertions travel in it rather than in dedicated fields.
func traceSuffix(ctx context.Context) string {
	tracer := pinpoint.FromContext(ctx)
	return fmt.Sprintf("|trace_id=%s|span_id=%s|sampled=%t",
		tracer.TransactionId().String(), e2e.SpanIDString(tracer), tracer.IsSampled())
}

type helloServer struct{}

func (helloServer) UnaryCallUnaryReturn(ctx context.Context, in *testapp.Greeting) (*testapp.Greeting, error) {
	tracer := pinpoint.FromContext(ctx)
	tracer.NewSpanEvent("HelloServer.UnaryCallUnaryReturn")
	tracer.SpanEvent().Annotations().AppendString(pinpoint.AnnotationApi, "UnaryCallUnaryReturn")
	defer tracer.EndSpanEvent()

	if in.GetMsg() == "force-error" {
		tracer.SpanEvent().SetError(fmt.Errorf("forced end-to-end test error"), "ForcedGrpcError")
		return nil, status.Error(codes.InvalidArgument, "forced end-to-end test error")
	}
	return &testapp.Greeting{Msg: "Unary response: " + in.GetMsg() + traceSuffix(ctx)}, nil
}

func (helloServer) UnaryCallStreamReturn(in *testapp.Greeting, stream testapp.Hello_UnaryCallStreamReturnServer) error {
	ctx := stream.Context()
	tracer := pinpoint.FromContext(ctx)
	tracer.NewSpanEvent("HelloServer.UnaryCallStreamReturn")
	defer tracer.EndSpanEvent()

	for i := 0; i < 3; i++ {
		msg := fmt.Sprintf("Stream response %d: %s%s", i, in.GetMsg(), traceSuffix(ctx))
		if err := stream.Send(&testapp.Greeting{Msg: msg}); err != nil {
			return err
		}
	}
	return nil
}

func (helloServer) StreamCallUnaryReturn(stream testapp.Hello_StreamCallUnaryReturnServer) error {
	ctx := stream.Context()
	tracer := pinpoint.FromContext(ctx)
	tracer.NewSpanEvent("HelloServer.StreamCallUnaryReturn")
	defer tracer.EndSpanEvent()

	var combined strings.Builder
	for {
		in, err := stream.Recv()
		if err != nil {
			break
		}
		combined.WriteString(in.GetMsg())
		combined.WriteString(" ")
	}
	return stream.SendAndClose(&testapp.Greeting{
		Msg: "Unary response: " + combined.String() + traceSuffix(ctx),
	})
}

func (helloServer) StreamCallStreamReturn(stream testapp.Hello_StreamCallStreamReturnServer) error {
	ctx := stream.Context()
	tracer := pinpoint.FromContext(ctx)
	tracer.NewSpanEvent("HelloServer.StreamCallStreamReturn")
	defer tracer.EndSpanEvent()

	for {
		in, err := stream.Recv()
		if err != nil {
			return nil
		}
		if err := stream.Send(&testapp.Greeting{Msg: "Echo: " + in.GetMsg() + traceSuffix(ctx)}); err != nil {
			return err
		}
	}
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	port := e2e.Port(os.Args[1:], 50051)

	e2e.ConfigureAgentEnvironment("go-e2e-grpc-downstream", "go-e2e-grpc-down")
	// The gRPC downstream records no URL statistics: it serves no URLs.
	e2e.SetDefaultEnv("PINPOINT_GO_HTTP_URLSTAT_ENABLE", "false")
	agent := e2e.StartAgent(e2e.ConfigFileOption())
	defer agent.Shutdown()

	listener, err := net.Listen("tcp", e2e.Addr(port))
	if err != nil {
		log.Fatalf("listen on %d: %v", port, err)
	}

	server := grpc.NewServer(
		grpc.UnaryInterceptor(ppgrpc.UnaryServerInterceptor()),
		grpc.StreamInterceptor(ppgrpc.StreamServerInterceptor()),
	)
	testapp.RegisterHelloServer(server, helloServer{})

	log.Printf("gRPC server started on %s (collector=%s)", e2e.Addr(port), e2e.CollectorHost())
	log.Printf("methods: UnaryCallUnaryReturn UnaryCallStreamReturn StreamCallUnaryReturn StreamCallStreamReturn")

	// The runner stops this server with SIGTERM; leave through gRPC's own
	// shutdown so the agent flushes what it has instead of dying mid-batch.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-stop
		server.GracefulStop()
	}()

	if err := server.Serve(listener); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
