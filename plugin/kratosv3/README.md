# ppkratosv3
This package instruments the [go-kratos/kratos/v3](https://github.com/go-kratos/kratos) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/kratosv3/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/kratosv3/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/kratosv3/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/kratosv3/v2)

This package instruments kratos servers and clients.

### server
To instrument a kratos server, use ServerMiddleware.

``` go
httpSrv := http.NewServer(
    http.Address(":8000"),
    http.Middleware(ppkratosv3.ServerMiddleware()),
)
```

The server middleware adds the pinpoint.Tracer to the kratos server handler's context.
By using the pinpoint.FromContext function, this tracer can be obtained.
Alternatively, the context of the handler may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
func (s *server) SayHello(ctx context.Context, in *helloworld.HelloRequest) (*helloworld.HelloReply, error) {
    tracer := pinpoint.FromContext(ctx)
    defer tracer.NewSpanEvent("f1").EndSpanEvent()
    ...
}
```
``` go
package main

import (
    "github.com/go-kratos/kratos/v3"
    "github.com/go-kratos/kratos/v3/transport"
    "github.com/go-kratos/kratos/v3/transport/http"
    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/kratosv3/v2"
)

type server struct {
    helloworld.UnimplementedGreeterServer
}

func (s *server) SayHello(ctx context.Context, in *helloworld.HelloRequest) (*helloworld.HelloReply, error) {
    tracer := pinpoint.FromContext(ctx)
    defer tracer.NewSpanEvent("f1").EndSpanEvent()

    return &helloworld.HelloReply{Message: fmt.Sprintf("Hello %+v", in.Name)}, nil
}

func main() {
    ... //setup agent
	
    s := &server{}
    httpSrv := http.NewServer(
        http.Address(":8000"),
        http.Middleware(
            ppkratosv3.ServerMiddleware(),
        ),
    )
    ...
}

```
[Full Example Source](/example/kratosv3/server/server.go)

### client
To instrument a kratos client, use ClientMiddleware.

``` go
conn, err := transhttp.NewClient(
    context.Background(),
    transhttp.WithMiddleware(ppkratosv3.ClientMiddleware()),
    transhttp.WithEndpoint("127.0.0.1:8000"),
)
```

It is necessary to pass the context containing the pinpoint.Tracer to kratos client.

``` go
client := pb.NewGreeterHTTPClient(conn)
reply, err := client.SayHello(pinpoint.NewContext(context.Background(), tracer), &pb.HelloRequest{Name: "kratos"})
```

``` go
import (
    transhttp "github.com/go-kratos/kratos/v3/transport/http"
    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/kratosv3/v2"
)

func callHTTP(w http.ResponseWriter, r *http.Request) {
    conn, err := transhttp.NewClient(
        context.Background(),
        transhttp.WithMiddleware(ppkratosv3.ClientMiddleware()),
        transhttp.WithEndpoint("127.0.0.1:8000"),
    )

    client := pb.NewGreeterHTTPClient(conn)
    reply, err := client.SayHello(r.Context(), &pb.HelloRequest{Name: "kratos"})
    ...
}
```
[Full Example Source](/example/kratosv3/client/client.go)

### gRPC streams
ServerMiddleware and ClientMiddleware trace unary calls and HTTP requests. Install
them with `Middleware` and `WithMiddleware`, not with kratos's `StreamMiddleware`
and `WithStreamMiddleware`. kratos runs a stream middleware once per
message, on whichever goroutine sends or receives it: the caller's tracer would
be shared by two goroutines and the request metadata written from both at once,
which stops the process with `fatal error: concurrent map writes`, and on the
server every message would become a span of its own, its normal end an error.
Trace a gRPC stream with the stream interceptors of [plugin/grpc](/plugin/grpc),
which kratos takes as options too.

``` go
srv := transgrpc.NewServer(
    transgrpc.Middleware(ppkratosv3.ServerMiddleware()),
    transgrpc.StreamInterceptor(ppgrpc.StreamServerInterceptor()),
)
```

``` go
// among the options of the gRPC client
transgrpc.WithMiddleware(ppkratosv3.ClientMiddleware()),
transgrpc.WithStreamInterceptor(ppgrpc.StreamClientInterceptor()),
```

### Notes

A handler error fails the span, as the gRPC plugin does: kratos reports a
`404` as an `errors.NotFound` error, so such a response is a failed
transaction here where it is not under the net/http plugins, whose
`Http.Server.RecordHandlerError` option does not reach this plugin. The remote
address of an HTTP transport is the socket's `RemoteAddr`; the
`Http.Server.RealIpHeader` option is not applied either.
