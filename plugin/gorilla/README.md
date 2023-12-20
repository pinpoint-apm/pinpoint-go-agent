# ppgorilla
This package instruments the [gorilla/mux](https://github.com/gorilla/mux) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorilla
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorilla"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorilla)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorilla)

This package instruments inbound requests handled by a gorilla mux.Router.
Register the Middleware as the middleware of the router to trace all handlers:

``` go
r := mux.NewRouter()
r.Use(ppgorilla.Middleware())
```

Use WrapHandler or WrapHandlerFunc to select the handlers you want to track:

``` go
r.HandleFunc("/outgoing", ppgorilla.WrapHandlerFunc(outGoing))
```

For each request, a pinpoint.Tracer is stored in the request context.
By using the pinpoint.FromContext function, this tracer can be obtained in your handler.
Alternatively, the context of the request may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
package main

import (
    "github.com/gorilla/mux"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorilla"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func hello(w http.ResponseWriter, r *http.Request) {
    io.WriteString(w, "hello world")
}

func main() {
    ... //setup agent
	
    r := mux.NewRouter()
    r.Use(ppgorilla.Middleware())

    //r.HandleFunc("/", ppgorilla.WrapHandlerFunc(hello)))
    http.ListenAndServe(":8000", r)
}

```
[Full Example Source](/plugin/gorilla/example/mux_server.go)

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
