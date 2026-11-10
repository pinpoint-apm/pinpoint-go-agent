# pphttp
Package pphttp instruments servers and clients that use net/http package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2)

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
    "context"
    "log"
    "net/http"
    "os"

    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
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
    cfg, _ := pinpoint.NewConfig(opts...)
    agent, err := pinpoint.NewAgent(cfg)
    if err != nil {
        log.Printf("pinpoint agent start failed: %v", err) // keeps running, untraced
    }
    defer agent.Shutdown()

    mux := pphttp.NewServeMux()
    mux.HandleFunc("/bar", outGoing)
    http.ListenAndServe("localhost:8000", mux)
}
```

This package supports URL Statistics feature. It aggregates response times, successes and failures for each router pattern.
But, WrapHandler and WrapHandlerFunc function doesn't support URL Statistics feature.

#### Config Options
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

### http client
This package instruments outbound requests and add distributed tracing headers.
Use WrapClient, WrapClientWithContext or DoClient.

``` go
client := pphttp.WrapClient(&http.Client{})
req, _ := http.NewRequestWithContext(r.Context(), "GET", external_url, nil)
client.Do(req)
```

`WrapClient` takes the tracer from each request's context, so `client.Get(url)`, whose request carries
`context.Background()`, records nothing. For that form, build the client with the context:

``` go
client := pphttp.WrapClientWithContext(r.Context(), &http.Client{})
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
    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
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
[Full Example Source](/example/http/http_server/http_server.go)

#### Config Options
* [Http.Client.RecordRequestHeader](/doc/config.md#httpclientrecordrequestheader)
* [Http.Client.RecordResponseHeader](/doc/config.md#httpclientrecordresponseheader)
* [Http.Client.RecordRequestCookie](/doc/config.md#httpclientrecordrequestcookie)

The full list of `Http.Client.*` options is in [Configuration](/doc/config.md#httpclientrecordrequestheader).
