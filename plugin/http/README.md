# pphttp
Package pphttp instruments servers and clients that use net/http package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/http
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/http)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/http)

### http server
This package instruments inbound requests handled by a http.ServeMux.
Use NewServeMux to trace all handlers:

``` go
mux := pphttp.NewServeMux()
mux.HandleFunc("/bar", outGoing)
```

Use WrapHandler or WrapHandlerFunc to select the handlers you want to track:

``` go
http.HandleFunc("/", pphttp.WrapHandlerFunc(index))
```

For each request, a pinpoint.Tracer is stored in the request context.
By using the pinpoint.FromContext function, this tracer can be obtained in your handler.
Alternatively, the context of the request may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
package main

import (
    "net/http"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func outGoing(w http.ResponseWriter, r *http.Request) {
    tracer := pinpoint.FromContext(r.Context())
    ctx := pinpoint.NewContext(context.Background(), tracer)
    // or ctx := r.Context()
    
    client := pphttp.WrapClientWithContext(ctx, &http.Client{})
    resp, err := client.Get("http://localhost:9000/async_wrapper?foo=bar&say=goodbye")
    ...
}

func main() {
    //setup agent
    opts := []pinpoint.ConfigOption{
        pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
    }
    cfg, err := pinpoint.NewConfig(opts...)
    agent, err := pinpoint.NewAgent(cfg)
    defer agent.Shutdown()

    mux := pphttp.NewServeMux()
    mux.HandleFunc("/bar", outGoing)
    http.ListenAndServe("localhost:8000", mux)
}
```

This package supports URL Statistics feature. It aggregates response times, successes and failures for each router pattern.
But, WrapHandler and WrapHandlerFunc function doesn't support URL Statistics feature.

#### Config Options
* [Http.Server.StatusCodeErrors](/doc/config.md#Http.Server.StatusCodeErrors)
* [Http.Server.ExcludeUrl](/doc/config.md#Http.Server.ExcludeUrl)
* [Http.Server.ExcludeMethod](/doc/config.md#Http.Server.ExcludeMethod)
* [Http.Server.RecordRequestHeader](/doc/config.md#Http.Server.RecordRequestHeader)
* [Http.Server.RecordResponseHeader](/doc/config.md#Http.Server.RecordResponseHeader)
* [Http.Server.RecordRequestCookie](/doc/config.md#Http.Server.RecordRequestCookie)
* [Http.UrlStat.Enable](/doc/config.md#Http.UrlStat.Enable)
* [Http.UrlStat.LimitSize](/doc/config.md#Http.UrlStat.LimitSize)

### http client
This package instruments outbound requests and add distributed tracing headers.
Use WrapClient, WrapClientWithContext or DoClient.

``` go
client := pphttp.WrapClient(&http.Client{})
client.Get(external_url)
```

``` go
req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
pphttp.DoClient(http.DefaultClient.Do, req)
```

It is necessary to pass the context containing the pinpoint.Tracer to the http.Request.

``` go
import (
    "net/http"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func wrapClient(w http.ResponseWriter, r *http.Request) {
    req, _ := http.NewRequestWithContext(r.Context(), "GET", "http://localhost:9000/async", nil)
    client := pphttp.WrapClient(&http.Client{})
    resp, err := client.Do(req)
    ...
}

func main() {
    ... //setup agent

    http.HandleFunc("/wrapclient", pphttp.WrapHandlerFunc(wrapClient))
    http.ListenAndServe(":8000", nil)
}

```
[Full Example Source](/plugin/http/example/http_server.go)

#### Config Options
* [Http.Client.RecordRequestHeader](/doc/config.md#Http.Client.RecordRequestHeader)
* [Http.Client.RecordResponseHeader](/doc/config.md#Http.Client.RecordResponseHeader)
* [Http.Client.RecordRequestCookie](/doc/config.md#Http.Client.RecordRequestCookie)
