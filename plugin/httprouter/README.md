# pphttprouter
This package instruments the [julienschmidt/httprouter](https://github.com/julienschmidt/httprouter) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/httprouter
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/httprouter"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/httprouter)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/httprouter)

This package instruments inbound requests handled by a httprouter.Router.
Use New() to trace all handlers:

``` go
r := pphttprouter.New()
r.GET("/", Index)
```

Use WrapHandle to select the handlers you want to track:

``` go
r := httprouter.New()
r.GET("/hello/:name", pphttprouter.WrapHandle(hello))
```

For each request, a pinpoint.Tracer is stored in the request context.
By using the pinpoint.FromContext function, this tracer can be obtained in your handler.
Alternatively, the context of the request may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
import (
    "github.com/julienschmidt/httprouter"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/httprouter"
)

func Index(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
    tracer := pinpoint.FromContext(r.Context())
    func() {
        defer tracer.NewSpanEvent("func_1").EndSpanEvent()
        time.Sleep(1 * time.Second)
    }()

    fmt.Fprint(w, "Welcome!\n")
}

func main() {
    ... //setup agent

    router := pphttprouter.New()
    router.GET("/", Index)
    http.ListenAndServe(":8000", router)
}
```
[Full Example Source](/plugin/httprouter/example/httprouter_server.go)

This package supports URL Statistics feature. It aggregates response times, successes and failures for each router pattern.
But, WrapHandle function doesn't support URL Statistics feature.

### Config Options
* [Http.Server.StatusCodeErrors](/doc/config.md#Http.Server.StatusCodeErrors)
* [Http.Server.ExcludeUrl](/doc/config.md#Http.Server.ExcludeUrl)
* [Http.Server.ExcludeMethod](/doc/config.md#Http.Server.ExcludeMethod)
* [Http.Server.RecordRequestHeader](/doc/config.md#Http.Server.RecordRequestHeader)
* [Http.Server.RecordResponseHeader](/doc/config.md#Http.Server.RecordResponseHeader)
* [Http.Server.RecordRequestCookie](/doc/config.md#Http.Server.RecordRequestCookie)
* [Http.UrlStat.Enable](/doc/config.md#Http.UrlStat.Enable)
* [Http.UrlStat.LimitSize](/doc/config.md#Http.UrlStat.LimitSize)
