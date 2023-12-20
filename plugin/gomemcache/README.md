# ppgomemcache
This package instruments the [bradfitz/gomemcache](https://github.com/bradfitz/gomemcache) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/gomemcache
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gomemcache"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/gomemcache)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/gomemcache)

This package instruments the gomemcache calls. Use the NewClient as the memcache.New.

``` go
mc := ppgomemcache.NewClient(addr...)
```

It is necessary to pass the context containing the pinpoint.Tracer to Client using Client.WithContext.

``` go
mc.WithContext(pinpoint.NewContext(context.Background(), tracer))
mc.Get("foo")
```

``` go
import (
    "github.com/bradfitz/gomemcache/memcache"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gomemcache"
)

func doMemcache(w http.ResponseWriter, r *http.Request) {
    addr := []string{"localhost:11211"}
    mc := ppgomemcache.NewClient(addr...)
    mc.WithContext(r.Context())

    item, err = mc.Get("foo")
	
    ...
}
```
[Full Example Source](/plugin/gomemcache/example/gomemcache_example.go)
