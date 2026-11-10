# ppgoredisv8
This package instruments the [go-redis/redis/v8](https://github.com/go-redis/redis) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv8/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv8/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv8/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv8/v2)

This package instruments the go-redis/v8 calls. Use the NewHook or NewClusterHook as the redis.Hook.
Only available in versions of go-redis with an AddHook() function.

``` go
rc := redis.NewClient(opts)
rc.AddHook(ppgoredisv8.NewHook(opts))
```

It is necessary to pass the context containing the pinpoint.Tracer with each command.
A pipeline is recorded on the context given to `Exec` (or `Pipelined`); the contexts of the queued commands are not used.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
val, err := rc.Get(ctx, "key").Result()

pipe := rc.Pipeline()
pipe.Incr(ctx, "counter")
cmds, err := pipe.Exec(ctx)
```

The span events report the address in the options as the endpoint. For a Sentinel, Ring or universal client,
give the hook a label of your own, `ppgoredisv8.NewHook(&redis.Options{Addr: "sentinel-master"})`;
`NewHook(nil)` reports `unknown`.

``` go
package main

import (
    "fmt"
    "net/http"

    "github.com/go-redis/redis/v8"
    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv8/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
)

var redisClient *redis.Client

func redisv8(w http.ResponseWriter, r *http.Request) {
    // r.Context() carries the tracer the HTTP plugin put there.
    ctx := r.Context()

    if err := redisClient.Set(ctx, "key", "value", 0).Err(); err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    val, err := redisClient.Get(ctx, "key").Result()
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    fmt.Fprintln(w, "key", val)
}

func main() {
    ... //setup agent

    addrs := []string{"localhost:6379", "localhost:6380"}

    redisOpts := &redis.Options{
        Addr: addrs[0],
    }
    redisClient = redis.NewClient(redisOpts)
    redisClient.AddHook(ppgoredisv8.NewHook(redisOpts))

    http.HandleFunc("/redis", pphttp.WrapHandlerFunc(redisv8))

    ...
}
```
[Full Example Source](/example/goredisv8/redisv8.go)
