# ppfasthttprouter
This package instruments the [fasthttp/router](https://github.com/fasthttp/router) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttprouter
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttprouter"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttprouter)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttprouter)

This package instruments inbound requests handled by a fasthttp/router.Router.
Use New() to trace all handlers:

``` go
r := ppfasthttprouter.New()
r.GET("/user/{name}", user)
```

For each request, a pinpoint.Tracer is stored in the request context.
By using the pinpoint.FromContext function, this tracer can be obtained in your handler.
Alternatively, the context of the request may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
import (
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttprouter"
    "github.com/valyala/fasthttp"
)

func user(ctx *fasthttp.RequestCtx) {
    tracer := pinpoint.FromContext(ctx.UserValue(ppfasthttp.CtxKey).(context.Context))
    defer tracer.NewSpanEvent("f1").EndSpanEvent()

    fmt.Fprintf(ctx, "hello, %s!\n", ctx.UserValue("name"))
}

func main() {
    ... //setup agent

    r := ppfasthttprouter.New()
    r.GET("/user/{name}", user)
    log.Fatal(fasthttp.ListenAndServe(":9000", r.Handler))
}
```
[Full Example Source](/plugin/fasthttprouter/example/fasthttprouter_server.go)

This package supports URL Statistics feature. It aggregates response times, successes and failures for each router pattern.

### Config Options
* [Http.Server.StatusCodeErrors](/doc/config.md#Http.Server.StatusCodeErrors)
* [Http.Server.ExcludeUrl](/doc/config.md#Http.Server.ExcludeUrl)
* [Http.Server.ExcludeMethod](/doc/config.md#Http.Server.ExcludeMethod)
* [Http.Server.RecordRequestHeader](/doc/config.md#Http.Server.RecordRequestHeader)
* [Http.Server.RecordResponseHeader](/doc/config.md#Http.Server.RecordResponseHeader)
* [Http.Server.RecordRequestCookie](/doc/config.md#Http.Server.RecordRequestCookie)
* [Http.UrlStat.Enable](/doc/config.md#Http.UrlStat.Enable)
* [Http.UrlStat.LimitSize](/doc/config.md#Http.UrlStat.LimitSize)
