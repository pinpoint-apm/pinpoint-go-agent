# ppgoredis
This package instruments the [go-redis/redis](https://github.com/go-redis/redis) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredis
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredis"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredis)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredis)

This package instruments the go-redis calls.
Use the NewClient as the redis.NewClient and the NewClusterClient as redis.NewClusterClient, respectively.
It supports go-redis v6.10.0 and later.

``` go
rc = ppgoredis.NewClient(redisOpts)
```

It is necessary to pass the context containing the pinpoint.Tracer to Client using Client.WithContext.

``` go
rc = rc.WithContext(pinpoint.NewContext(context.Background(), tracer))
rc.Pipeline()
```

``` go
package main

import (
    "github.com/go-redis/redis"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredis"
)

var redisClient *ppgoredis.Client
var redisClusterClient *ppgoredis.ClusterClient

func redisv6(w http.ResponseWriter, r *http.Request) {
    c := redisClient.WithContext(r.Context())
    redisPipeIncr(c.Pipeline())
}

func redisv6Cluster(w http.ResponseWriter, r *http.Request) {
    c := redisClusterClient.WithContext(r.Context())
    redisPipeIncr(c.Pipeline())
}

func redisPipeIncr(pipe redis.Pipeliner) {
    incr := pipe.Incr("foo")
    pipe.Expire("foo", time.Hour)
    _, er := pipe.Exec()
    fmt.Println(incr.Val(), er)
}

func main() {
    ... //setup agent

    addrs := []string {"localhost:6379", "localhost:6380"}

    //redis client
    redisOpts := &redis.Options{
        Addr: addrs[0],
    }
    redisClient = ppgoredis.NewClient(redisOpts)

    //redis cluster client
    redisClusterOpts := &redis.ClusterOptions{
        Addrs: addrs,
    }
    redisClusterClient = ppgoredis.NewClusterClient(redisClusterOpts)

    http.HandleFunc("/redis", phttp.WrapHandlerFunc(redisv6))
    http.HandleFunc("/rediscluster", phttp.WrapHandlerFunc(redisv6Cluster))

    ...
}
```
[Full Example Source](/plugin/goredis/example/redisv6.go)
