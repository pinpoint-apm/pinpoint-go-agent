package main

import (
	"context"
	"io"
	"log"
	"net"
	"os"

	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	pgrpc "github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/example/testapp"
	"google.golang.org/grpc"
)

type Server struct{}

var returnMsg = &testapp.Greeting{Msg: "Hello!!"}

func (s *Server) UnaryCallUnaryReturn(ctx context.Context, msg *testapp.Greeting) (*testapp.Greeting, error) {
	printGreeting(ctx, msg)
	return returnMsg, nil
}

func (s *Server) UnaryCallStreamReturn(in *testapp.Greeting, stream testapp.Hello_UnaryCallStreamReturnServer) error {
	printGreeting(stream.Context(), in)

	for i := 0; i < 2; i++ {
		if err := stream.Send(returnMsg); err != nil {
			return err
		}
	}

	return nil
}

func (s *Server) StreamCallUnaryReturn(stream testapp.Hello_StreamCallUnaryReturnServer) error {
	for {
		in, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(returnMsg)
		}
		if err != nil {
			return err
		}

		printGreeting(stream.Context(), in)
	}
}

func (s *Server) StreamCallStreamReturn(stream testapp.Hello_StreamCallStreamReturnServer) error {
	for {
		in, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		printGreeting(stream.Context(), in)

		if err := stream.Send(returnMsg); err != nil {
			return err
		}
	}
}

func printGreeting(ctx context.Context, in *testapp.Greeting) {
	defer pinpoint.FromContext(ctx).NewSpanEvent("printGreeting").EndSpanEvent()
	log.Println(in.Msg)
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("TestGrpcServer"),
		pinpoint.WithAgentId("TestGrpcServerAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	listener, err := net.Listen("tcp", "localhost:8080")
	if err != nil {
		panic(err)
	}

	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(pgrpc.UnaryServerInterceptor(agent)),
		grpc.StreamInterceptor(pgrpc.StreamServerInterceptor(agent)),
	)
	testapp.RegisterHelloServer(grpcServer, &Server{})
	grpcServer.Serve(listener)
}
