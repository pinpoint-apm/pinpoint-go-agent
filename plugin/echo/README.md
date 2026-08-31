# ppecho
This package instruments the [labstack/echo](https://github.com/labstack/echo) package.

> **Warning**
> echo v3 is end-of-life upstream, and
> [GHSA-vfp3-v2gw-7wfq](https://github.com/advisories/GHSA-vfp3-v2gw-7wfq)
> (high: an encoded-slash `%2F` bypass of route-level protection that exposes
> static files) covers **every** v3 release including the last one, `v3.3.10`.
> There is no patched v3 to pin.
>
> The vulnerable code is not reachable from this plugin - it instruments
> requests and never serves static files - but the pin is the floor your
> application inherits, and your application does serve routes.
>
> Use [plugin/echov4](/plugin/echov4) or [plugin/echov5](/plugin/echov5)
> instead. This package remains for applications still on echo v3.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/echo
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/echo"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/echo)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/echo)

This package instruments inbound requests handled by an echo.Router.
Register the Middleware as the middleware of the router to trace all handlers:

``` go
e := echo.New()
e.Use(ppecho.Middleware())
```

Use WrapHandler to select the handlers you want to track:

``` go
e.GET("/hello", ppecho.WrapHandler(hello))
```

For each request, a pinpoint.Tracer is stored in the request context.
By using the pinpoint.FromContext function, this tracer can be obtained in your handler.
Alternatively, the context of the request may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
package main

import (
    "github.com/labstack/echo"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/echo"
)

func hello(c echo.Context) error {
    return c.String(200, "Hello World!!")
}

func main() {
    ... //setup agent
	
    e := echo.New()
    e.Use(ppecho.Middleware())

    e.GET("/hello", hello)
    e.Start(":9000")
}

```
[Full Example Source](/plugin/echo/example/echo_server.go)

This package supports URL Statistics feature. It aggregates response times, successes and failures for each router pattern.

### Config Options
* [Http.Server.StatusCodeErrors](/doc/config.md#Http.Server.StatusCodeErrors)
* [Http.Server.ExcludeUrl](/doc/config.md#Http.Server.ExcludeUrl)
* [Http.Server.ExcludeMethod](/doc/config.md#Http.Server.ExcludeMethod)
* [Http.Server.RecordRequestHeader](/doc/config.md#Http.Server.RecordRequestHeader)
* [Http.Server.RecordResponseHeader](/doc/config.md#Http.Server.RecordResponseHeader)
* [Http.Server.RecordRequestCookie](/doc/config.md#Http.Server.RecordRequestCookie)
* [Http.Server.RecordHandlerError](/doc/config.md#Http.Server.RecordHandlerError)
* [Http.UrlStat.Enable](/doc/config.md#Http.UrlStat.Enable)
* [Http.UrlStat.LimitSize](/doc/config.md#Http.UrlStat.LimitSize)
