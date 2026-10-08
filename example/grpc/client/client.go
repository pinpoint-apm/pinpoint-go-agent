package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/testapp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var greeting = &testapp.Greeting{Msg: "Hello!"}

func unaryCallUnaryReturn(ctx context.Context, client testapp.HelloClient) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	in, err := client.UnaryCallUnaryReturn(ctx, greeting)
	if err != nil {
		log.Printf("unaryCallUnaryReturn got error %v", err)
		return
	}
	log.Println(in.Msg)
}

func unaryCallStreamReturn(ctx context.Context, client testapp.HelloClient) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	stream, err := client.UnaryCallStreamReturn(ctx, greeting)
	if err != nil {
		log.Printf("unaryCallStreamReturn got error %v", err)
		return
	}

	for {
		in, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("unaryCallStreamReturn got error %v", err)
			return
		}
		log.Println(in.Msg)
	}
}

func streamCallUnaryReturn(ctx context.Context, client testapp.HelloClient) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	stream, err := client.StreamCallUnaryReturn(ctx)
	if err != nil {
		log.Printf("streamCallUnaryReturn got error %v", err)
		return
	}

	for i := 0; i < 2; i++ {
		if err := stream.Send(greeting); err != nil {
			if err == io.EOF {
				break
			}
			log.Printf("streamCallUnaryReturn got error %v", err)
			break
		}
	}

	msg, err := stream.CloseAndRecv()
	if err != nil {
		log.Printf("streamCallUnaryReturn got error %v", err)
		return
	}
	log.Println(msg.Msg)
}

func streamCallStreamReturn(ctx context.Context, client testapp.HelloClient) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	stream, err := client.StreamCallStreamReturn(ctx)
	if err != nil {
		log.Printf("streamCallStreamReturn got error %v", err)
		return
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
				log.Printf("streamCallStreamReturn got error %v", err)
				close(waitc)
				return
			}
			log.Println(in.Msg)
		}
	}()

	for i := 0; i < 2; i++ {
		if err := stream.Send(greeting); err != nil {
			log.Printf("streamCallStreamReturn got error %v", err)
			break
		}
	}
	stream.CloseSend()
	<-waitc
}

func doGrpc(w http.ResponseWriter, r *http.Request) {
	conn, err := grpc.NewClient(
		"dns:///localhost:8080",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(ppgrpc.UnaryClientInterceptor()),
		grpc.WithStreamInterceptor(ppgrpc.StreamClientInterceptor()),
	)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	client := testapp.NewHelloClient(conn)
	ctx := r.Context() // carries the request's tracer; no need to rewrap it

	unaryCallUnaryReturn(ctx, client)
	unaryCallStreamReturn(ctx, client)
	streamCallUnaryReturn(ctx, client)
	streamCallStreamReturn(ctx, client)
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("TestGrpcClient"),
		pinpoint.WithAgentName("TestGrpcClientAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()
	defer pinpoint.ShutdownOnSignal(agent)() // SIGTERM, SIGINT

	http.HandleFunc("/grpc", pphttp.WrapHandlerFunc(doGrpc))
	http.ListenAndServe(":9000", nil)
}
