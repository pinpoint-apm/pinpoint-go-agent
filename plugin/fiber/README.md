# ppfiber
This package instruments the [gofiber/fiber/v2](https://github.com/gofiber/fiber) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/fiber
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fiber"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/fiber)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/fiber)

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
Alternatively, the context of the request may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
package main

import (
    "github.com/gofiber/fiber/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fiber"
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
    log.Fatal(app.Listen(":9000"))
}
```
[Full Example Source](/plugin/fiber/example/fiber_server.go)

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
