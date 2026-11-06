# ppbeego
This package instruments the [beego/v2](https://github.com/beego/beego) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/beego/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/beego/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/beego/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/beego/v2)

### server
This package instruments inbound requests handled by a beego instance.
Register the ServerFilterChain as the filter chain of the router to trace all handlers:

``` go
web.InsertFilterChain("/*", ppbeego.ServerFilterChain())
```

For each request, a pinpoint.Tracer is stored in the request context.
By using the pinpoint.FromContext function, this tracer can be obtained in your handler.
Alternatively, the context of the request may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
package main

import (
    "github.com/beego/beego/v2/server/web"
    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/beego/v2"
)

func (m *MainController) Hello() {
    tracer := pinpoint.FromContext(m.Ctx.Request.Context())
    defer tracer.NewSpanEvent("f1").EndSpanEvent()

    m.Ctx.WriteString("hello, world!!")
}

func main() {
    //setup agent
 
	ctrl := &MainController{}
	web.Router("/hello", ctrl, "get:Hello")
	web.InsertFilterChain("/*", ppbeego.ServerFilterChain())
	web.Run("localhost:9000")
}
```

This package supports URL Statistics feature. It aggregates response times, successes and failures for each router pattern.

#### Config Options
* [Http.Server.StatusCodeErrors](/doc/config.md#httpserverstatuscodeerrors)
* [Http.Server.ExcludeUrl](/doc/config.md#httpserverexcludeurl)
* [Http.Server.ExcludeMethod](/doc/config.md#httpserverexcludemethod)
* [Http.Server.RecordRequestHeader](/doc/config.md#httpserverrecordrequestheader)
* [Http.Server.RecordResponseHeader](/doc/config.md#httpserverrecordresponseheader)
* [Http.Server.RecordRequestCookie](/doc/config.md#httpserverrecordrequestcookie)
* [Http.UrlStat.Enable](/doc/config.md#httpurlstatenable)
* [Http.UrlStat.LimitSize](/doc/config.md#httpurlstatlimitsize)

The full list of `Http.Server.*` and `Http.UrlStat.*` options is in [Configuration](/doc/config.md#httpserverstatuscodeerrors).

### client
This package instruments outbound requests and adds distributed tracing headers.
Register `ClientFilterChain(nil)` once, at startup, and build each request with a context that carries the
tracer: the filter reads the tracer from the request's own context.

``` go
import (
	"github.com/beego/beego/v2/client/httplib"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/beego/v2"
)

func main() {
    // ... start the agent
    httplib.AddDefaultFilter(ppbeego.ClientFilterChain(nil)) // before requests are built
    // ... run the server
}

func (m *MainController) Get() {
    req := httplib.NewBeegoRequestWithCtx(m.Ctx.Request.Context(), "http://localhost:9090/", "GET")
    str, _ := req.String()
    m.Ctx.WriteString(str)
}
```
[Full Example Source](/example/beego/beego_server.go)

Outside a traced handler, put the tracer in the context yourself with `pinpoint.NewContext(ctx, tracer)`. A request
built with `httplib.Get()` or `NewBeegoRequest()` carries `context.Background()` and is not traced.

Do not add a filter built with a tracer through `req.AddFilters`. httplib copies its default settings into every
request, spare capacity included, so `AddFilters` appends into an array the requests share: concurrent requests
run each other's filters, record on each other's tracers and send each other's trace ids.

#### Config Options
* [Http.Client.RecordRequestHeader](/doc/config.md#httpclientrecordrequestheader)
* [Http.Client.RecordResponseHeader](/doc/config.md#httpclientrecordresponseheader)
* [Http.Client.RecordRequestCookie](/doc/config.md#httpclientrecordrequestcookie)

The full list of `Http.Client.*` options is in [Configuration](/doc/config.md#httpclientrecordrequestheader).
