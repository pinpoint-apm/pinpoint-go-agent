// It is modified the source code from kratos example
// https://github.com/go-kratos/examples/tree/main/helloworld

package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/go-kratos/kratos/v2"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/transport"
	"github.com/go-kratos/kratos/v2/transport/grpc"
	"github.com/go-kratos/kratos/v2/transport/http"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/kratos"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/kratos/example/helloworld"
)

// server is used to implement helloworld.GreeterServer.
type server struct {
	helloworld.UnimplementedGreeterServer
}

// SayHello implements helloworld.GreeterServer
func (s *server) SayHello(ctx context.Context, in *helloworld.HelloRequest) (*helloworld.HelloReply, error) {
	tracer := pinpoint.FromContext(ctx)
	defer tracer.NewSpanEvent("f1").EndSpanEvent()
	defer tracer.NewSpanEvent("f2").EndSpanEvent()

	tr, _ := transport.FromServerContext(ctx)
	log.Printf("[server] Endpoint %s\n", tr.Endpoint())

	if in.Name == "error" {
		return nil, errors.BadRequest("custom_error", fmt.Sprintf("invalid argument %s", in.Name))
	}
	if in.Name == "panic" {
		panic("server panic")
	}
	return &helloworld.HelloReply{Message: fmt.Sprintf("Hello %+v", in.Name)}, nil
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("TestKratosServer"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	s := &server{}
	httpSrv := http.NewServer(
		http.Address(":8000"),
		http.Middleware(
			ppkratos.ServerMiddleware(),
		),
	)
	grpcSrv := grpc.NewServer(
		grpc.Address(":9000"),
		grpc.Middleware(
			ppkratos.ServerMiddleware(),
		),
	)
	helloworld.RegisterGreeterServer(grpcSrv, s)
	helloworld.RegisterGreeterHTTPServer(httpSrv, s)

	app := kratos.New(
		kratos.Name("helloworld"),
		kratos.Server(
			httpSrv,
			grpcSrv,
		),
	)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
