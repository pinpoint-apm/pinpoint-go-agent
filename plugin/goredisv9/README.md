# ppgoredisv9
This package instruments the [go-redis/redis/v9](https://github.com/go-redis/redis) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv9
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv9"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv9)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv9)

This package instruments the go-redis/v9 calls. Use the NewHook or NewClusterHook as the redis.Hook.
Only available in versions of go-redis with an AddHook() function.

``` go
rc = redis.NewClient(redisOpts)
client.AddHook(ppgoredisv9.NewHook(opts))
```

It is necessary to pass the context containing the pinpoint.Tracer to redis.Client.

``` go
rc = rc.WithContext(pinpoint.NewContext(context.Background(), tracer))
rc.Pipeline()
```

``` go
package main

import (
    "github.com/go-redis/redis/v9"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv9"
)

var redisClient *redis.Client

func redisv9(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    client := redisClient

    err := client.Set(ctx, "key", "value", 0).Err()
    val, err := client.Get(ctx, "key").Result()
    fmt.Println("key", val)
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
[Full Example Source](/plugin/goredisv9/example/redisv9.go)
