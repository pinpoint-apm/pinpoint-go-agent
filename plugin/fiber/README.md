# ppfiber
This package instruments the [gofiber/fiber/v2](https://github.com/gofiber/fiber) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/fiber/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fiber/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/fiber/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/fiber/v2)

This package instruments inbound requests handled by a fiber instance.
Register the Middleware as the middleware of the router to trace all handlers:

``` go
app := fiber.New()
app.Use(ppfiber.Middleware())
```

Use WrapHandler to select the handlers you want to track:

``` go
app.Get("/hello", ppfiber.WrapHandler(hello))
```

For each request, a pinpoint.Tracer is stored in the user context.
By using the pinpoint.FromContext function, this tracer can be obtained in your handler.
Alternatively, pass `c.UserContext()` wherever a context carrying the pinpoint.Tracer is required.
`c.Context()` is the `*fasthttp.RequestCtx` and carries no tracer.

``` go
package main

import (
    "log"

    "github.com/gofiber/fiber/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fiber/v2"
)

func hello(c *fiber.Ctx) error {
    tracer := pinpoint.FromContext(c.UserContext())
    defer tracer.NewSpanEvent("f1").EndSpanEvent()

    return c.SendString("Hello, World !!")
}

func main() {
    ... //setup agent
	
    app := fiber.New()
    app.Use(ppfiber.Middleware())
    app.Get("/hello", hello)
    log.Fatal(app.Listen(":9000"))
}
```
[Full Example Source](/example/fiber/fiber_server.go)

This package supports URL Statistics feature. It aggregates response times, successes and failures for each router pattern.

### Config Options
* [Http.Server.StatusCodeErrors](/doc/config.md#httpserverstatuscodeerrors)
* [Http.Server.ExcludeUrl](/doc/config.md#httpserverexcludeurl)
* [Http.Server.ExcludeMethod](/doc/config.md#httpserverexcludemethod)
* [Http.Server.RecordRequestHeader](/doc/config.md#httpserverrecordrequestheader)
* [Http.Server.RecordResponseHeader](/doc/config.md#httpserverrecordresponseheader)
* [Http.Server.RecordRequestCookie](/doc/config.md#httpserverrecordrequestcookie)
* [Http.Server.RecordHandlerError](/doc/config.md#httpserverrecordhandlererror)
* [Http.Server.ProxyUserHeaderNames](/doc/config.md#httpserverproxyuserheadernames)
* [Http.UrlStat.Enable](/doc/config.md#httpurlstatenable)
* [Http.UrlStat.LimitSize](/doc/config.md#httpurlstatlimitsize)
