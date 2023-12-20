# ppgin
This package instruments the [gin-gonic/gin](https://github.com/gin-gonic/gin) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/gin
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gin"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/gin)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/gin)

This package instruments inbound requests handled by a gin.Engine.
Register the Middleware as the middleware of the router to trace all handlers:

``` go
r := gin.Default()
r.Use(ppgin.Middleware())
```

Use WrapHandler to select the handlers you want to track:

``` go
r.GET("/external", ppgin.WrapHandler(extCall))
```

For each request, a pinpoint.Tracer is stored in the request context.
By using the pinpoint.FromContext function, this tracer can be obtained in your handler.
Alternatively, the context of the request may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
package main

import (
    "github.com/gin-gonic/gin"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gin"
)

func endpoint(c *gin.Context) {
    c.Writer.WriteString("endpoint")
}

func main() {
    ... //setup agent
	
    router := gin.Default()
    router.Use(ppgin.Middleware())

    router.GET("/endpoint", endpoint)
    router.Run(":8000")
}

```
[Full Example Source](/plugin/gin/example/gin_server.go)

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
