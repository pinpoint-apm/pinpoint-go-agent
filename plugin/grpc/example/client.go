package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	pgrpc "github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/example/testapp"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"google.golang.org/grpc"
)

var greeting = &testapp.Greeting{Msg: "Hello!"}

func unaryCallUnaryReturn(ctx context.Context, client testapp.HelloClient) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	in, err := client.UnaryCallUnaryReturn(ctx, greeting)
	if err != nil {
		log.Fatalf("unaryCallUnaryReturn got error %v", err)
	}
	log.Println(in.Msg)
}

func unaryCallStreamReturn(ctx context.Context, client testapp.HelloClient) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	stream, err := client.UnaryCallStreamReturn(ctx, greeting)
	if err != nil {
		log.Fatalf("unaryCallStreamReturn got error %v", err)
	}

	for {
		in, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatalf("unaryCallStreamReturn got error %v", err)
		}
		log.Println(in.Msg)
	}
}

func streamCallUnaryReturn(ctx context.Context, client testapp.HelloClient) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	stream, err := client.StreamCallUnaryReturn(ctx)
	if err != nil {
		log.Fatalf("streamCallUnaryReturn got error %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := stream.Send(greeting); err != nil {
			if err == io.EOF {
				break
			}
			log.Fatalf("streamCallUnaryReturn got error %v", err)
		}
	}

	msg, err := stream.CloseAndRecv()
	if err != nil {
		log.Fatalf("streamCallUnaryReturn got error %v", err)
	}
	log.Println(msg.Msg)
}

func streamCallStreamReturn(ctx context.Context, client testapp.HelloClient) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	stream, err := client.StreamCallStreamReturn(ctx)
	if err != nil {
		log.Fatalf("streamCallStreamReturn got error %v", err)
	}

	waitc := make(chan struct{})
	go func() {
		for {
			in, err := stream.Recv()
			if err == io.EOF {
				close(waitc)
				return
			}
			if err != nil {
				log.Fatalf("streamCallStreamReturn got error %v", err)
			}
			log.Println(in.Msg)
		}
	}()

	for i := 0; i < 2; i++ {
		if err := stream.Send(greeting); err != nil {
			log.Fatalf("streamCallStreamReturn got error %v", err)
		}
	}
	stream.CloseSend()
	<-waitc
}

func doGrpc(w http.ResponseWriter, r *http.Request) {
	conn, err := grpc.Dial(
		"localhost:8080",
		grpc.WithInsecure(),
		grpc.WithUnaryInterceptor(pgrpc.UnaryClientInterceptor()),
		grpc.WithStreamInterceptor(pgrpc.StreamClientInterceptor()),
	)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	client := testapp.NewHelloClient(conn)
	tracer := pinpoint.FromContext(r.Context())
	ctx := pinpoint.NewContext(context.Background(), tracer)

	unaryCallUnaryReturn(ctx, client)
	unaryCallStreamReturn(ctx, client)
	streamCallUnaryReturn(ctx, client)
	streamCallStreamReturn(ctx, client)
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("TestGrpcClient"),
		pinpoint.WithAgentId("TestGrpcClientAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	http.HandleFunc(phttp.WrapHandleFunc("/grpc", doGrpc))
	http.ListenAndServe(":9000", nil)
}
