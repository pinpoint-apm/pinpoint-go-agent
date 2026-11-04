# ppchi
This package instruments the [go-chi/chi](https://github.com/go-chi/chi) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/chi/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/chi/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/chi/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/chi/v2)

This package instruments inbound requests handled by a chi.Router.
Register the Middleware as the middleware of the router to trace all handlers:

``` go
r := chi.NewRouter()
r.Use(ppchi.Middleware())
```

Use WrapHandler or WrapHandlerFunc to select the handlers you want to track:

``` go
r.Get("/hello", ppchi.WrapHandlerFunc(hello))
```

For each request, a pinpoint.Tracer is stored in the request context.
By using the pinpoint.FromContext function, this tracer can be obtained in your handler.
Alternatively, the context of the request may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
import (
    "io"
    "net/http"

    "github.com/go-chi/chi/v5"
    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/chi/v2"
)

func hello(w http.ResponseWriter, r *http.Request) {
    io.WriteString(w, "hello world")
}

func main() {
    ... //setup agent
	
    r := chi.NewRouter()
    r.Use(ppchi.Middleware())
    
    r.Get("/hello", hello)
    http.ListenAndServe(":8000", r)
}
```

[Full Example Source](/example/chi/chi_server.go)

This package supports URL Statistics feature. It aggregates response times, successes and failures for each router pattern.
The pattern is read from chi v5's route context (`github.com/go-chi/chi/v5`): requests routed by an older chi
are still traced, but their URL statistics land under `/NULL`.
Register the middleware after chi's `Recoverer` (`r.Use(middleware.Recoverer, ppchi.Middleware())`), so a panic is
recorded before `Recoverer` turns it into a 500; see
[Where the middleware goes](/doc/plugin_guide.md#where-the-middleware-goes).

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
