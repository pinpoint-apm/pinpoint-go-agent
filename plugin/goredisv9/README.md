# ppgoredisv9
This package instruments the [redis/go-redis/v9](https://github.com/redis/go-redis) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv9/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv9/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv9/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv9/v2)

This package instruments the go-redis/v9 calls. Use the NewHook or NewClusterHook as the redis.Hook.
Only available in versions of go-redis with an AddHook() function.

``` go
rc := redis.NewClient(opts)
rc.AddHook(ppgoredisv9.NewHook(opts))
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
give the hook a label of your own, `ppgoredisv9.NewHook(&redis.Options{Addr: "sentinel-master"})`;
`NewHook(nil)` reports `unknown`.

The commands go-redis's AutoPipeliner batches are sent with a context of its
own and are not traced. One it sends apart from the batch, such as a blocking
command, is traced like any other command on the default, blocking
AutoPipeliner (`AutoPipeline()`), which runs it on the caller's goroutine. On
the async one (`AsyncAutoPipeline()`) it runs on a goroutine of its own and is
recorded on a goroutine tracer, which needs a span event open in the caller:
sent with none open, it is not recorded.

``` go
package main

import (
    "fmt"
    "net/http"

    "github.com/redis/go-redis/v9"
    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv9/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
)

var redisClient *redis.Client

func redisv9(w http.ResponseWriter, r *http.Request) {
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
    redisClient.AddHook(ppgoredisv9.NewHook(redisOpts))

    http.HandleFunc("/redis", pphttp.WrapHandlerFunc(redisv9))

    ...
}
```
[Full Example Source](/example/goredisv9/redisv9.go)
