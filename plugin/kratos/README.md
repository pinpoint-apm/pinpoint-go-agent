# ppkratos
This package instruments the [go-kratos/kratos/v2](https://github.com/go-kratos/kratos) package.

> **Note**
> [GHSA-jj45-xvq5-rhh9](https://github.com/advisories/GHSA-jj45-xvq5-rhh9)
> (medium: a confused deputy issue) covers every kratos v2 release through
> `v2.9.2`, and there is no patched v2.
>
> The vulnerable code is not reachable from this plugin, but the pin is the
> floor your application inherits. [plugin/kratosv3](/plugin/kratosv3) is the
> upgrade path.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/kratos/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/kratos/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/kratos/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/kratos/v2)

This package instruments kratos servers and clients.

### server
To instrument a kratos server, use ServerMiddleware.

``` go
httpSrv := http.NewServer(
    http.Address(":8000"),
    http.Middleware(ppkratos.ServerMiddleware()),
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
    "context"
    "fmt"
    "log"

    "github.com/go-kratos/kratos/v2"
    "github.com/go-kratos/kratos/v2/transport/http"
    "github.com/pinpoint-apm/pinpoint-go-agent/example/kratos/helloworld"
    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/kratos/v2"
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
            ppkratos.ServerMiddleware(),
        ),
    )
    helloworld.RegisterGreeterHTTPServer(httpSrv, s)

    app := kratos.New(kratos.Name("helloworld"), kratos.Server(httpSrv))
    if err := app.Run(); err != nil {
        log.Fatal(err)
    }
}
```
[Full Example Source](/example/kratos/server/server.go)

### client
To instrument a kratos client, use ClientMiddleware.

``` go
conn, err := transhttp.NewClient(
    context.Background(),
    transhttp.WithMiddleware(ppkratos.ClientMiddleware()),
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
    "context"
    "net/http"

    transhttp "github.com/go-kratos/kratos/v2/transport/http"
    pb "github.com/pinpoint-apm/pinpoint-go-agent/example/kratos/helloworld"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/kratos/v2"
)

func callHTTP(w http.ResponseWriter, r *http.Request) {
    conn, err := transhttp.NewClient(
        context.Background(),
        transhttp.WithMiddleware(ppkratos.ClientMiddleware()),
        transhttp.WithEndpoint("127.0.0.1:8000"),
    )
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    defer conn.Close()

    client := pb.NewGreeterHTTPClient(conn)
    reply, err := client.SayHello(r.Context(), &pb.HelloRequest{Name: "kratos"})
    ...
}
```
[Full Example Source](/example/kratos/client/client.go)

### gRPC streams
ServerMiddleware and ClientMiddleware trace unary calls and HTTP requests. Install
them with `Middleware` and `WithMiddleware`, not with kratos's `StreamMiddleware`
and `WithStreamMiddleware` (kratos v2.9 and later). kratos runs a stream middleware once per
message, on whichever goroutine sends or receives it: the caller's tracer would
be shared by two goroutines and the request metadata written from both at once,
which stops the process with `fatal error: concurrent map writes`, and on the
server every message would become a span of its own, its normal end an error.
Trace a gRPC stream with the stream interceptors of [plugin/grpc](/plugin/grpc),
which kratos takes as options too.

``` go
srv := transgrpc.NewServer(
    transgrpc.Middleware(ppkratos.ServerMiddleware()),
    transgrpc.StreamInterceptor(ppgrpc.StreamServerInterceptor()),
)
```

``` go
// among the options of the gRPC client
transgrpc.WithMiddleware(ppkratos.ClientMiddleware()),
transgrpc.WithStreamInterceptor(ppgrpc.StreamClientInterceptor()),
```

### Notes

ServerMiddleware does not go through the [http plugin](/plugin/http), so an
HTTP request is recorded as a kratos call too: the span is named after the
kratos operation (`/helloworld.Greeter/SayHello`), not the path, and no HTTP
status is recorded - the outcome is the error the handler returns. None of the
`Http.Server.*` options applies (`ExcludeUrl`, `ExcludeMethod`,
`StatusCodeErrors`, `RecordHandlerError`, header, cookie and parameter
recording, `RealIpHeader`): the remote address of an HTTP transport is the
socket's `RemoteAddr`. No URL statistics are collected.

A handler error fails the span, as the gRPC plugin does, so a handler that
returns `errors.NotFound` (a `404`) makes a failed transaction. So does a 404
an echo or fiber handler returns as an error, by default
([Http.Server.RecordHandlerError](/doc/config.md#httpserverrecordhandlererror));
the net/http, chi and gorilla plugins judge a 404 by its status alone, which
the default `Http.Server.StatusCodeErrors` (`5xx`) does not count. A path no
route matches is answered outside the middleware and is not traced at all.
