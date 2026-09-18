// A go-kit gRPC service traced with plugin/grpc. go-kit's gRPC transport is a
// plain grpc-go service handler, so the ordinary ppgrpc interceptors trace it
// and put the tracer in the endpoint's context - no go-kit plugin is needed.
package main

import (
	"context"
	"log"
	"net"
	"os"

	"github.com/go-kit/kit/endpoint"
	grpctransport "github.com/go-kit/kit/transport/grpc"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/example/testapp"
	"google.golang.org/grpc"
)

// makeHelloEndpoint is the go-kit business endpoint. The context already
// carries the tracer the ppgrpc server interceptor started.
func makeHelloEndpoint() endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		defer pinpoint.FromContext(ctx).NewSpanEvent("helloEndpoint").EndSpanEvent()
		return "Hello, " + request.(string) + "!", nil
	}
}

// helloBinding adapts the generated HelloServer interface to go-kit's Handler.
type helloBinding struct {
	testapp.UnimplementedHelloServer
	hello grpctransport.Handler
}

func (b *helloBinding) UnaryCallUnaryReturn(ctx context.Context, req *testapp.Greeting) (*testapp.Greeting, error) {
	_, resp, err := b.hello.ServeGRPC(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.(*testapp.Greeting), nil
}

func decodeRequest(_ context.Context, req interface{}) (interface{}, error) {
	return req.(*testapp.Greeting).Msg, nil
}

func encodeResponse(_ context.Context, resp interface{}) (interface{}, error) {
	return &testapp.Greeting{Msg: resp.(string)}, nil
}

func main() {
	cfg, _ := pinpoint.NewConfig(
		pinpoint.WithAppName("GoKitGrpcServer"),
		pinpoint.WithAgentName("GoKitGrpcServerAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME")+"/tmp/pinpoint-config.yaml"),
	)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	listener, err := net.Listen("tcp", "localhost:8080")
	if err != nil {
		log.Fatal(err)
	}

	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(ppgrpc.UnaryServerInterceptor()),
		grpc.StreamInterceptor(ppgrpc.StreamServerInterceptor()),
	)
	testapp.RegisterHelloServer(grpcServer, &helloBinding{
		hello: grpctransport.NewServer(makeHelloEndpoint(), decodeRequest, encodeResponse),
	})
	log.Fatal(grpcServer.Serve(listener))
}
