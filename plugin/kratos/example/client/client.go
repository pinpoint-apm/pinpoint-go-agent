// It is modified the source code from kratos example
// https://github.com/go-kratos/examples/tree/main/helloworld

package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/go-kratos/kratos/v2/errors"
	transgrpc "github.com/go-kratos/kratos/v2/transport/grpc"
	transhttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/kratos"
	pb "github.com/pinpoint-apm/pinpoint-go-agent/plugin/kratos/example/helloworld"
)

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("TestKratosClient"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	http.HandleFunc("/http", pphttp.WrapHandlerFunc(callHTTP))
	http.HandleFunc("/grpc", pphttp.WrapHandlerFunc(callGRPC))
	http.ListenAndServe(":9900", nil)
}

func callHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := transhttp.NewClient(
		context.Background(),
		transhttp.WithMiddleware(
			ppkratos.ClientMiddleware(),
		),
		transhttp.WithEndpoint("127.0.0.1:8000"),
	)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	tracer := pinpoint.FromContext(r.Context())
	ctx := pinpoint.NewContext(context.Background(), tracer)

	client := pb.NewGreeterHTTPClient(conn)
	reply, err := client.SayHello(ctx, &pb.HelloRequest{Name: "kratos"})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("[http] SayHello %s\n", reply.Message)

	// returns error
	_, err = client.SayHello(ctx, &pb.HelloRequest{Name: "error"})
	if err != nil {
		log.Printf("[http] SayHello error: %v\n", err)
	}
	if errors.IsBadRequest(err) {
		log.Printf("[http] SayHello error is invalid argument: %v\n", err)
	}
}

func callGRPC(w http.ResponseWriter, r *http.Request) {
	conn, err := transgrpc.DialInsecure(
		context.Background(),
		transgrpc.WithEndpoint("127.0.0.1:9000"),
		transgrpc.WithMiddleware(
			ppkratos.ClientMiddleware(),
		),
	)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	tracer := pinpoint.FromContext(r.Context())
	ctx := pinpoint.NewContext(context.Background(), tracer)

	client := pb.NewGreeterClient(conn)
	reply, err := client.SayHello(ctx, &pb.HelloRequest{Name: "kratos"})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("[grpc] SayHello %+v\n", reply)

	// returns error
	_, err = client.SayHello(ctx, &pb.HelloRequest{Name: "error"})
	if err != nil {
		log.Printf("[grpc] SayHello error: %v\n", err)
	}
	if errors.IsBadRequest(err) {
		log.Printf("[grpc] SayHello error is invalid argument: %v\n", err)
	}
}
