# ppbeego
This package instruments the [beego/v2](https://github.com/beego/beego) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/beego
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/beego"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/beego)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/beego)

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
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/beego"
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
* [Http.Server.StatusCodeErrors](/doc/config.md#Http.Server.StatusCodeErrors)
* [Http.Server.ExcludeUrl](/doc/config.md#Http.Server.ExcludeUrl)
* [Http.Server.ExcludeMethod](/doc/config.md#Http.Server.ExcludeMethod)
* [Http.Server.RecordRequestHeader](/doc/config.md#Http.Server.RecordRequestHeader)
* [Http.Server.RecordResponseHeader](/doc/config.md#Http.Server.RecordResponseHeader)
* [Http.Server.RecordRequestCookie](/doc/config.md#Http.Server.RecordRequestCookie)
* [Http.UrlStat.Enable](/doc/config.md#Http.UrlStat.Enable)
* [Http.UrlStat.LimitSize](/doc/config.md#Http.UrlStat.LimitSize)

### client
This package instruments outbound requests and add distributed tracing headers.
Add the ClientFilterChain as the filter chain of the request.

``` go
req := httplib.Get("http://localhost:9090/")
req.AddFilters(ppbeego.ClientFilterChain(tracer))
```
``` go
import (
	"github.com/beego/beego/v2/client/httplib"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/beego"
)

func (m *MainController) Get() {
    tracer := pinpoint.FromContext(m.Ctx.Request.Context())

    req := httplib.Get("http://localhost:9090/")
    req.AddFilters(ppbeego.ClientFilterChain(tracer))
    str, _ := req.String()
    m.Ctx.WriteString(str)
}
```
[Full Example Source](/plugin/beego/example/beego_server.go)

#### Config Options
* [Http.Client.RecordRequestHeader](/doc/config.md#Http.Client.RecordRequestHeader)
* [Http.Client.RecordResponseHeader](/doc/config.md#Http.Client.RecordResponseHeader)
* [Http.Client.RecordRequestCookie](/doc/config.md#Http.Client.RecordRequestCookie)
