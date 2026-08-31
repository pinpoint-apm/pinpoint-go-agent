# ppfiberv3
This package instruments the [gofiber/fiber/v3](https://github.com/gofiber/fiber) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/fiberv3
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fiberv3"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/fiberv3)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/fiberv3)

This package instruments inbound requests handled by a fiber instance.
Register the Middleware as the middleware of the router to trace all handlers:

``` go
app := fiber.New()
app.Use(ppfiberv3.Middleware())
```

Use WrapHandler to select the handlers you want to track:

``` go
app.Get("/hello", ppfiberv3.WrapHandler(hello))
```

For each request, a pinpoint.Tracer is stored in the request context.
By using the pinpoint.FromContext function, this tracer can be obtained in your handler.
Alternatively, the context of the request may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
package main

import (
    "github.com/gofiber/fiber/v3"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fiberv3"
)

func hello(c fiber.Ctx) error {
    tracer := pinpoint.FromContext(c.Context())
    defer tracer.NewSpanEvent("f1").EndSpanEvent()

    return c.SendString("Hello, World !!")
}

func main() {
    ... //setup agent
	
    app := fiber.New()
    app.Use(ppfiberv3.Middleware())
    log.Fatal(app.Listen(":9000"))
}
```
[Full Example Source](/plugin/fiberv3/example/fiberv3_server.go)

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
