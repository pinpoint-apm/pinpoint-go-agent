# ppfasthttp
This package instruments the [valyala/fasthttp](https://github.com/valyala/fasthttp) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp/v2)

### server
This package instruments inbound requests handled by a fasthttp instance.
Use WrapHandler to select the handlers you want to track:

``` go
fasthttp.ListenAndServe(":9000", ppfasthttp.WrapHandler(requestHandler))
```

WrapHandler stores a context.Context carrying the pinpoint.Tracer as the user value `ppfasthttp.CtxKey` of the
handler's `*fasthttp.RequestCtx`. Pass that context wherever a context with the tracer is required (DoClient, a
database query, an outgoing gRPC call). The `*fasthttp.RequestCtx` itself is a context.Context too, but the wrong
one: its Value reads the request's user values, so `pinpoint.FromContext(ctx)` finds no tracer and the call records
nothing. See [Getting the tracer in a handler](/doc/plugin_guide.md#getting-the-tracer-in-a-handler).

WrapHandler sets the user value on every request it wraps, the agent disabled included. A handler that may also run
unwrapped asserts it with comma-ok: an unchecked assertion panics there, and fasthttp does not recover a handler
panic.

``` go
package main

import (
    "context"
    "fmt"

    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp/v2"
    "github.com/valyala/fasthttp"
)

func requestHandler(ctx *fasthttp.RequestCtx) {
    tctx, ok := ctx.UserValue(ppfasthttp.CtxKey).(context.Context)
    if !ok {
        tctx = context.Background() // not wrapped: this request is not traced
    }
    tracer := pinpoint.FromContext(tctx)
    defer tracer.NewSpanEvent("f1").EndSpanEvent()

    fmt.Fprintf(ctx, "Hello, world!\n\n")
}

func main() {
    ... //setup agent
	
    fasthttp.ListenAndServe(":9000", ppfasthttp.WrapHandler(requestHandler))
}

```
[Full Example Source](/example/fasthttp/fasthttp_server.go)

This package supports URL Statistics feature. It aggregates response times, successes and failures for each pattern given as parameter of a WrapHandler function.
A handler wrapped without a pattern collects no URL statistics.

### Config Options
* [Http.Server.StatusCodeErrors](/doc/config.md#httpserverstatuscodeerrors)
* [Http.Server.ExcludeUrl](/doc/config.md#httpserverexcludeurl)
* [Http.Server.ExcludeMethod](/doc/config.md#httpserverexcludemethod)
* [Http.Server.RecordRequestHeader](/doc/config.md#httpserverrecordrequestheader)
* [Http.Server.RecordResponseHeader](/doc/config.md#httpserverrecordresponseheader)
* [Http.Server.RecordRequestCookie](/doc/config.md#httpserverrecordrequestcookie)
* [Http.Server.ProxyUserHeaderNames](/doc/config.md#httpserverproxyuserheadernames)
* [Http.UrlStat.Enable](/doc/config.md#httpurlstatenable)
* [Http.UrlStat.LimitSize](/doc/config.md#httpurlstatlimitsize)

The full list of `Http.Server.*` and `Http.UrlStat.*` options is in [Configuration](/doc/config.md#httpserverstatuscodeerrors).

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
[Full Example Source](/example/fasthttp/fasthttp_server.go)

#### Config Options
* [Http.Client.RecordRequestHeader](/doc/config.md#httpclientrecordrequestheader)
* [Http.Client.RecordResponseHeader](/doc/config.md#httpclientrecordresponseheader)
* [Http.Client.RecordRequestCookie](/doc/config.md#httpclientrecordrequestcookie)

The full list of `Http.Client.*` options is in [Configuration](/doc/config.md#httpclientrecordrequestheader).
