// A go-kit HTTP service traced with plugin/http, calling the go-kit gRPC
// server through a go-kit gRPC client traced with plugin/grpc.
//
// go-kit's HTTP Server is an http.Handler, so pphttp.WrapHandler traces it
// and the tracer travels to the endpoint in the request context. go-kit's
// HTTP client accepts any client with a Do method, so pphttp.WrapClient
// traces it. Neither needs a go-kit plugin.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"

	"github.com/go-kit/kit/endpoint"
	grpctransport "github.com/go-kit/kit/transport/grpc"
	httptransport "github.com/go-kit/kit/transport/http"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/example/testapp"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type helloResponse struct {
	Msg string `json:"msg"`
}

// GET /hello?name=x  ->  go-kit gRPC client  ->  GoKitGrpcServer
func makeHelloEndpoint(conn *grpc.ClientConn) endpoint.Endpoint {
	call := grpctransport.NewClient(conn, "Hello", "UnaryCallUnaryReturn",
		func(_ context.Context, req interface{}) (interface{}, error) {
			return &testapp.Greeting{Msg: req.(string)}, nil
		},
		func(_ context.Context, resp interface{}) (interface{}, error) {
			return resp.(*testapp.Greeting).Msg, nil
		},
		testapp.Greeting{},
	).Endpoint()

	return func(ctx context.Context, request interface{}) (interface{}, error) {
		defer pinpoint.FromContext(ctx).NewSpanEvent("helloEndpoint").EndSpanEvent()
		msg, err := call(ctx, request)
		if err != nil {
			return nil, err
		}
		return helloResponse{Msg: msg.(string)}, nil
	}
}

// GET /proxy?name=x  ->  go-kit HTTP client  ->  /hello on this same server
func makeProxyEndpoint() endpoint.Endpoint {
	target, _ := url.Parse("http://localhost:8000/hello")
	return httptransport.NewClient(http.MethodGet, target,
		func(_ context.Context, r *http.Request, req interface{}) error {
			r.URL.RawQuery = url.Values{"name": {req.(string)}}.Encode()
			return nil
		},
		func(_ context.Context, r *http.Response) (interface{}, error) {
			var resp helloResponse
			return resp, json.NewDecoder(r.Body).Decode(&resp)
		},
		httptransport.SetClient(pphttp.WrapClient(nil)),
	).Endpoint()
}

func decodeName(_ context.Context, r *http.Request) (interface{}, error) {
	return r.URL.Query().Get("name"), nil
}

func main() {
	cfg, _ := pinpoint.NewConfig(
		pinpoint.WithAppName("GoKitHttpGateway"),
		pinpoint.WithAgentName("GoKitHttpGatewayAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME")+"/tmp/pinpoint-config.yaml"),
	)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	conn, err := grpc.NewClient("localhost:8080",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(ppgrpc.UnaryClientInterceptor()),
		grpc.WithStreamInterceptor(ppgrpc.StreamClientInterceptor()),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	http.Handle("/hello", pphttp.WrapHandler(
		httptransport.NewServer(makeHelloEndpoint(conn), decodeName, httptransport.EncodeJSONResponse)))
	http.Handle("/proxy", pphttp.WrapHandler(
		httptransport.NewServer(makeProxyEndpoint(), decodeName, httptransport.EncodeJSONResponse)))
	log.Fatal(http.ListenAndServe(":8000", nil))
}
