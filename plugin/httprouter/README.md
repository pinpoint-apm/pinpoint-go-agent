# pphttprouter
This package instruments the [julienschmidt/httprouter](https://github.com/julienschmidt/httprouter) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/httprouter/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/httprouter/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/httprouter/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/httprouter/v2)

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
    "fmt"
    "net/http"
    "time"

    "github.com/julienschmidt/httprouter"
    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/httprouter/v2"
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
[Full Example Source](/example/httprouter/httprouter_server.go)

This package supports URL Statistics feature. It aggregates response times, successes and failures for each router pattern.
WrapHandle collects URL Statistics only when it is given the route pattern as its second argument, e.g. `pphttprouter.WrapHandle(hello, "/hello/:name")`; without it the statistics are skipped.

### Config Options
* [Http.Server.StatusCodeErrors](/doc/config.md#httpserverstatuscodeerrors)
* [Http.Server.ExcludeUrl](/doc/config.md#httpserverexcludeurl)
* [Http.Server.ExcludeMethod](/doc/config.md#httpserverexcludemethod)
* [Http.Server.RecordRequestHeader](/doc/config.md#httpserverrecordrequestheader)
* [Http.Server.RecordResponseHeader](/doc/config.md#httpserverrecordresponseheader)
* [Http.Server.RecordRequestCookie](/doc/config.md#httpserverrecordrequestcookie)
* [Http.UrlStat.Enable](/doc/config.md#httpurlstatenable)
* [Http.UrlStat.LimitSize](/doc/config.md#httpurlstatlimitsize)
