# ppfasthttp
This package instruments the [valyala/fasthttp](https://github.com/valyala/fasthttp) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp)

### server
This package instruments inbound requests handled by a fasthttp instance.
Use WrapHandler to select the handlers you want to track:

``` go
fasthttp.ListenAndServe(":9000", ppfasthttp.WrapHandler(requestHandler))
```

WrapHandler sets the pinpoint.Tracer as a user value of fasthttp handler's context.
By using the ppfasthttp.CtxKey, this tracer can be obtained in your handler.
Alternatively, the context of the handler may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
package main

import (
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp"
    "github.com/valyala/fasthttp"
)

func requestHandler(ctx *fasthttp.RequestCtx) {
    tracer := pinpoint.FromContext(ctx.UserValue(ppfasthttp.CtxKey).(context.Context))
    defer tracer.NewSpanEvent("f1").EndSpanEvent()

    fmt.Fprintf(ctx, "Hello, world!\n\n")
}

func main() {
    ... //setup agent
	
    fasthttp.ListenAndServe(":9000", ppfasthttp.WrapHandler(requestHandler))
}

```
[Full Example Source](/plugin/fasthttp/example/fasthttp_server.go)

This package supports URL Statistics feature. It aggregates response times, successes and failures for each pattern given as parameter of a WrapHandler function.

### Config Options
* [Http.Server.StatusCodeErrors](/doc/config.md#Http.Server.StatusCodeErrors)
* [Http.Server.ExcludeUrl](/doc/config.md#Http.Server.ExcludeUrl)
* [Http.Server.ExcludeMethod](/doc/config.md#Http.Server.ExcludeMethod)
* [Http.Server.RecordRequestHeader](/doc/config.md#Http.Server.RecordRequestHeader)
* [Http.Server.RecordResponseHeader](/doc/config.md#Http.Server.RecordResponseHeader)
* [Http.Server.RecordRequestCookie](/doc/config.md#Http.Server.RecordRequestCookie)
* [Http.Server.RecordHandlerError](/doc/config.md#Http.Server.RecordHandlerError)
* [Http.UrlStat.Enable](/doc/config.md#Http.UrlStat.Enable)
* [Http.UrlStat.LimitSize](/doc/config.md#Http.UrlStat.LimitSize)

### client
This package instruments outbound requests and add distributed tracing headers.
Use DoClient.

``` go
err := ppfasthttp.DoClient(func() error {
    return hc.Do(req, resp)
}, ctx, req, resp)
```

It is necessary to pass the context containing the pinpoint.Tracer to DoClient.

``` go
func client(ctx *fasthttp.RequestCtx) {
    url := fasthttp.AcquireURI()
    url.Parse(nil, []byte("http://localhost:8080/"))

    hc := &fasthttp.HostClient{Addr: "localhost:8080"}
    req := fasthttp.AcquireRequest()
    req.SetURI(url)
    resp := fasthttp.AcquireResponse()

    ctxWithTracer := ctx.UserValue(ppfasthttp.CtxKey).(context.Context)
    err := ppfasthttp.DoClient(func() error {
        return hc.Do(req, resp)
    }, ctxWithTracer, req, resp)

    ...
}
```
[Full Example Source](/plugin/fasthttp/example/fasthttp_server.go)

#### Config Options
* [Http.Client.RecordRequestHeader](/doc/config.md#Http.Client.RecordRequestHeader)
* [Http.Client.RecordResponseHeader](/doc/config.md#Http.Client.RecordResponseHeader)
* [Http.Client.RecordRequestCookie](/doc/config.md#Http.Client.RecordRequestCookie)
