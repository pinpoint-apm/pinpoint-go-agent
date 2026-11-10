# ppgomemcache
This package instruments the [bradfitz/gomemcache](https://github.com/bradfitz/gomemcache) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/gomemcache/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gomemcache/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/gomemcache/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/gomemcache/v2)

This package instruments the gomemcache calls. Use the NewClient as the memcache.New.

``` go
mc := ppgomemcache.NewClient(addr...)
```

It is necessary to pass the context containing the pinpoint.Tracer to Client using Client.WithContext.
WithContext returns a per-request copy; use that copy for the request's calls and keep the original client shared.

``` go
c := mc.WithContext(pinpoint.NewContext(context.Background(), tracer))
c.Get("foo")
```

`Get`, `GetMulti`, `Set`, `Add`, `Replace`, `CompareAndSwap`, `Delete`, `DeleteAll`, `FlushAll`, `Increment`,
`Decrement`, `Touch` and `Ping` are traced; `Append`, `Prepend` and `GetAndTouch` reach the embedded
`*memcache.Client` and are not. A client created elsewhere (`memcache.NewFromSelector`, ...) is instrumented
with `ppgomemcache.WrapClient(client, endpoint)`.

``` go
import (
    "net/http"

    "github.com/bradfitz/gomemcache/memcache"
    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gomemcache/v2"
)

var mc = ppgomemcache.NewClient("localhost:11211") // created once and shared

func doMemcache(w http.ResponseWriter, r *http.Request) {
    c := mc.WithContext(r.Context()) // a per-request copy bound to the request's tracer

    item, err := c.Get("foo")
	
    ...
}
```
[Full Example Source](/example/gomemcache/gomemcache_example.go)
