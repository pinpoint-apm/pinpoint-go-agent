# ppgrpc
This package instruments the [grpc/grpc-go](https://github.com/grpc/grpc-go) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc)

This package instruments gRPC servers and clients.

### server
To instrument a gRPC server, use UnaryServerInterceptor and StreamServerInterceptor.

``` go
grpcServer := grpc.NewServer(
    grpc.UnaryInterceptor(ppgrpc.UnaryServerInterceptor()),
    grpc.StreamInterceptor(ppgrpc.StreamServerInterceptor()),
)
```

ppgrpc's server interceptor adds the pinpoint.Tracer to the gRPC server handler's context.
By using the pinpoint.FromContext function, this tracer can be obtained.
Alternatively, the context of the handler may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
func (s *Server) UnaryCallUnaryReturn(ctx context.Context, msg *testapp.Greeting) (*testapp.Greeting, error) {
    tracer := pinpoint.FromContext(ctx)
    tracer.NewSpanEvent("gRPC Server Handler").EndSpanEvent()
    return "hi", nil
}
```

``` go
package main

import (
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/example/testapp"
    "google.golang.org/grpc"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc"
)

type Server struct{}
var returnMsg = &testapp.Greeting{Msg: "Hello!!"}

func (s *Server) UnaryCallUnaryReturn(ctx context.Context, msg *testapp.Greeting) (*testapp.Greeting, error) {
    printGreeting(ctx, msg)
    return returnMsg, nil
}

func (s *Server) StreamCallUnaryReturn(stream testapp.Hello_StreamCallUnaryReturnServer) error {
    for {
        in, err := stream.Recv()
        printGreeting(stream.Context(), in)
    }
}

func printGreeting(ctx context.Context, in *testapp.Greeting) {
    defer pinpoint.FromContext(ctx).NewSpanEvent("printGreeting").EndSpanEvent()
    log.Println(in.Msg)
}

func main() {
    ... //setup agent
	
    grpcServer := grpc.NewServer(
        grpc.UnaryInterceptor(ppgrpc.UnaryServerInterceptor()),
        grpc.StreamInterceptor(ppgrpc.StreamServerInterceptor()),
    )
    testapp.RegisterHelloServer(grpcServer, &Server{})
    grpcServer.Serve(listener)
}

```
[Full Example Source](/plugin/grpc/example/server.go)

### client
To instrument a gRPC client, use UnaryClientInterceptor and StreamClientInterceptor.

``` go
conn, err := grpc.Dial(
    "localhost:8080",
    grpc.WithUnaryInterceptor(ppgrpc.UnaryClientInterceptor()),
    grpc.WithStreamInterceptor(ppgrpc.StreamClientInterceptor()),
)
```

It is necessary to pass the context containing the pinpoint.Tracer to grpc.Client.

``` go
client := testapp.NewHelloClient(conn)
client.UnaryCallUnaryReturn(pinpoint.NewContext(context.Background(), tracer), greeting)
```

``` go
import (
    "google.golang.org/grpc"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/example/testapp"
)

func unaryCallUnaryReturn(ctx context.Context, client testapp.HelloClient) {
    in, err := client.UnaryCallUnaryReturn(ctx, greeting)
    log.Println(in.Msg)
}

func streamCallUnaryReturn(ctx context.Context, client testapp.HelloClient) {
    stream, err := client.StreamCallUnaryReturn(ctx)

    for i := 0; i < numStreamSend; i++ {
        if err := stream.Send(greeting); err != nil {
            break
        }
    }

    ...
}

func doGrpc(w http.ResponseWriter, r *http.Request) {
    conn, err := grpc.Dial(
        "localhost:8080",
        grpc.WithInsecure(),
        grpc.WithUnaryInterceptor(ppgrpc.UnaryClientInterceptor()),
        grpc.WithStreamInterceptor(ppgrpc.StreamClientInterceptor()),
    )
    defer conn.Close()

    ctx := r.Context()
    unaryCallUnaryReturn(ctx, client)
    streamCallUnaryReturn(ctx, client)
}
```
[Full Example Source](/plugin/grpc/example/client.go)
