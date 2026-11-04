# ppfasthttprouter
This package instruments the [fasthttp/router](https://github.com/fasthttp/router) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttprouter/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttprouter/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttprouter/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttprouter/v2)

This package instruments inbound requests handled by a fasthttp/router.Router.
Use New() to trace all handlers:

``` go
r := ppfasthttprouter.New()
r.GET("/user/{name}", user)
```

For each request, the wrapped handler stores a context.Context carrying the pinpoint.Tracer as the user value
`ppfasthttp.CtxKey` of the `*fasthttp.RequestCtx`, as [ppfasthttp](/plugin/fasthttp) does.
Get the tracer from that context, and pass the context wherever one with the tracer is required.
The `*fasthttp.RequestCtx` itself is not that context: `pinpoint.FromContext(ctx)` finds no tracer in it.
See [Getting the tracer in a handler](/doc/plugin_guide.md#getting-the-tracer-in-a-handler).

``` go
import (
    "context"
    "fmt"
    "log"

    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttprouter/v2"
    "github.com/valyala/fasthttp"
)

func user(ctx *fasthttp.RequestCtx) {
    tctx, ok := ctx.UserValue(ppfasthttp.CtxKey).(context.Context)
    if !ok {
        tctx = context.Background() // not wrapped: this request is not traced
    }
    tracer := pinpoint.FromContext(tctx)
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
[Full Example Source](/example/fasthttprouter/fasthttprouter_server.go)

The routes of a group are traced as well, nested groups included, and the URL statistics get the full pattern,
the group's prefix and the route's path:

``` go
api := r.Group("/api")
api.GET("/users/{id}", user) // counted under /api/users/{id}
```

The file-serving routes (`ServeFiles`, `ServeFS`, `ServeFilesCustom`) are not traced, on the Router or on a group.

This package supports URL Statistics feature. It aggregates response times, successes and failures for each router pattern.
A request no route matches is not traced.

### Config Options
* [Http.Server.StatusCodeErrors](/doc/config.md#httpserverstatuscodeerrors)
* [Http.Server.ExcludeUrl](/doc/config.md#httpserverexcludeurl)
* [Http.Server.ExcludeMethod](/doc/config.md#httpserverexcludemethod)
* [Http.Server.RecordRequestHeader](/doc/config.md#httpserverrecordrequestheader)
* [Http.Server.RecordResponseHeader](/doc/config.md#httpserverrecordresponseheader)
* [Http.Server.RecordRequestCookie](/doc/config.md#httpserverrecordrequestcookie)
* [Http.UrlStat.Enable](/doc/config.md#httpurlstatenable)
* [Http.UrlStat.LimitSize](/doc/config.md#httpurlstatlimitsize)

The full list of `Http.Server.*` and `Http.UrlStat.*` options is in [Configuration](/doc/config.md#httpserverstatuscodeerrors).
