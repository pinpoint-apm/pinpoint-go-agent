# ppgrpc
This package instruments the [grpc/grpc-go](https://github.com/grpc/grpc-go) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/v2)

This package instruments gRPC servers and clients.

### server
To instrument a gRPC server, use UnaryServerInterceptor and StreamServerInterceptor.

``` go
grpcServer := grpc.NewServer(
    grpc.UnaryInterceptor(ppgrpc.UnaryServerInterceptor()),
    grpc.StreamInterceptor(ppgrpc.StreamServerInterceptor()),
)
```

With other interceptors, list ppgrpc's first: a call that a later one rejects (authentication, rate limiting) is
then traced too, and the later ones find the tracer in the context.

``` go
grpcServer := grpc.NewServer(
    grpc.ChainUnaryInterceptor(ppgrpc.UnaryServerInterceptor(), auth),
    grpc.ChainStreamInterceptor(ppgrpc.StreamServerInterceptor(), streamAuth),
)
```

ppgrpc's server interceptor adds the pinpoint.Tracer to the gRPC server handler's context.
By using the pinpoint.FromContext function, this tracer can be obtained.
Alternatively, the context of the handler may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
func (s *Server) UnaryCallUnaryReturn(ctx context.Context, msg *testapp.Greeting) (*testapp.Greeting, error) {
    tracer := pinpoint.FromContext(ctx)
    tracer.NewSpanEvent("gRPC Server Handler").EndSpanEvent()
    return &testapp.Greeting{Msg: "hi"}, nil
}
```

``` go
package main

import (
    "context"
    "io"
    "log"
    "net"

    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/v2/test/testapp"
    "google.golang.org/grpc"
)

type Server struct {
    testapp.UnimplementedHelloServer
}

var returnMsg = &testapp.Greeting{Msg: "Hello!!"}

func (s *Server) UnaryCallUnaryReturn(ctx context.Context, msg *testapp.Greeting) (*testapp.Greeting, error) {
    printGreeting(ctx, msg)
    return returnMsg, nil
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

func printGreeting(ctx context.Context, in *testapp.Greeting) {
    defer pinpoint.FromContext(ctx).NewSpanEvent("printGreeting").EndSpanEvent()
    log.Println(in.Msg)
}

func main() {
    ... //setup agent

    listener, err := net.Listen("tcp", "localhost:8080")
    if err != nil {
        log.Fatal(err)
    }
    grpcServer := grpc.NewServer(
        grpc.UnaryInterceptor(ppgrpc.UnaryServerInterceptor()),
        grpc.StreamInterceptor(ppgrpc.StreamServerInterceptor()),
    )
    testapp.RegisterHelloServer(grpcServer, &Server{})
    grpcServer.Serve(listener)
}
```
[Full Example Source](/example/grpc/server/server.go)

### client
To instrument a gRPC client, use UnaryClientInterceptor and StreamClientInterceptor.

``` go
conn, err := grpc.NewClient(
    "localhost:8080",
    grpc.WithTransportCredentials(insecure.NewCredentials()),
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
    "context"
    "log"
    "net/http"

    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/v2/test/testapp"
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
)

var greeting = &testapp.Greeting{Msg: "Hello!"}

func unaryCallUnaryReturn(ctx context.Context, client testapp.HelloClient) {
    in, err := client.UnaryCallUnaryReturn(ctx, greeting)
    if err != nil {
        log.Println(err)
        return
    }
    log.Println(in.Msg)
}

func streamCallUnaryReturn(ctx context.Context, client testapp.HelloClient) {
    stream, err := client.StreamCallUnaryReturn(ctx)
    if err != nil {
        log.Println(err)
        return
    }

    for i := 0; i < 3; i++ {
        if err := stream.Send(greeting); err != nil {
            break
        }
    }

    ...
}

var client testapp.HelloClient // built once in main and shared

func doGrpc(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context() // carries the tracer the HTTP plugin put there
    unaryCallUnaryReturn(ctx, client)
    streamCallUnaryReturn(ctx, client)
}

func main() {
    ... //setup agent

    conn, err := grpc.NewClient(
        "localhost:8080",
        grpc.WithTransportCredentials(insecure.NewCredentials()),
        grpc.WithUnaryInterceptor(ppgrpc.UnaryClientInterceptor()),
        grpc.WithStreamInterceptor(ppgrpc.StreamClientInterceptor()),
    )
    if err != nil {
        log.Fatal(err)
    }
    defer conn.Close()
    client = testapp.NewHelloClient(conn)

    http.HandleFunc("/grpc", pphttp.WrapHandlerFunc(doGrpc))
    http.ListenAndServe(":8000", nil)
}
```
[Full Example Source](/example/grpc/client/client.go)

### Notes

A handler error fails the span: the error is the RPC's status, so unlike the
net/http plugins there is no `Http.Server.RecordHandlerError` switch here.

Every method is traced, health checks (`/grpc.health.v1.Health/Check`, and
`Watch`, a stream that lasts as long as the client watches) and server
reflection included. The `Http.Server.*` options, `ExcludeUrl` among them, do
not reach gRPC, and no URL statistics are collected. To leave a method out,
call the interceptor conditionally (the same works for the stream one):

``` go
pp := ppgrpc.UnaryServerInterceptor()
skipHealth := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
    if strings.HasPrefix(info.FullMethod, "/grpc.health.v1.Health/") {
        return h(ctx, req)
    }
    return pp(ctx, req, info, h)
}
grpcServer := grpc.NewServer(grpc.ChainUnaryInterceptor(skipHealth /*, auth, ... */))
```

A stream is one transaction: its span ends when the stream handler returns, so
it reaches the UI when the stream closes, with the stream's lifetime as its
response time. A handler that sends on one goroutine and receives on another
gives the second its own tracer
(`pinpoint.FromContext(stream.Context()).NewGoroutineTracer()`). See
[Long-lived connections](/doc/plugin_guide.md#long-lived-connections).
