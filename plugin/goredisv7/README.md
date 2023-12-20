# ppgoredisv7
This package instruments the [go-redis/redis/v7](https://github.com/go-redis/redis) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv7
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv7"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv7)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv7)

This package instruments the go-redis/v7 calls. Use the NewHook or NewClusterHook as the redis.Hook.
Only available in versions of go-redis with an AddHook() function.

``` go
rc = redis.NewClient(redisOpts)
client.AddHook(ppgoredisv7.NewHook(opts))
```

It is necessary to pass the context containing the pinpoint.Tracer to redis.Client.

``` go
rc = rc.WithContext(pinpoint.NewContext(context.Background(), tracer))
rc.Pipeline()
```

``` go
package main

import (
    "github.com/go-redis/redis/v7"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv7"
)

var redisClient *redis.Client

func redisv7(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    client := redisClient.WithContext(ctx)

    err := client.Set("key", "value", 0).Err()
    val, err := client.Get("key").Result()
    fmt.Println("key", val)
}

func main() {
    ... //setup agent

    addrs := []string{"localhost:6379", "localhost:6380"}

    redisOpts := &redis.Options{
        Addr: addrs[0],
    }
    redisClient = redis.NewClient(redisOpts)
    redisClient.AddHook(ppgoredisv7.NewHook(redisOpts))

    http.HandleFunc("/redis", pphttp.WrapHandlerFunc(redisv7))

    ...
}
```
[Full Example Source](/plugin/goredisv7/example/redisv7.go)
