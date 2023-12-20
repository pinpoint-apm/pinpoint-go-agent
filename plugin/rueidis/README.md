# pprueidis
This package instruments the [redis/rueidis](https://github.com/redis/rueidis) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/rueidis
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/rueidis"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/rueidis)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/rueidis)

This package instruments the redis/rueidis calls.
Use the NewHook as the rueidis.Hook.

``` go
client, err := rueidis.NewClient(opt)
client = rueidishook.WithHook(client, pprueidis.NewHook(opt))
```

It is necessary to pass the context containing the pinpoint.Tracer to rueidis.Client.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
err = client.Do(ctx, client.B().Set().Key("foo").Value("bar").Nx().Build()).Error()
```

``` go
package main

import (
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/rueidis"
    "github.com/redis/rueidis"
    "github.com/redis/rueidis/rueidishook"
)

func rueidisv1(w http.ResponseWriter, r *http.Request) {
    opt := rueidis.ClientOption{
        InitAddress: []string{"localhost:6379"},
    }

    client, err := rueidis.NewClient(opt)
    if err != nil {
		fmt.Println(err)
    }
    client = rueidishook.WithHook(client, pprueidis.NewHook(opt))

    ctx := r.Context()
    err = client.Do(ctx, client.B().Set().Key("foo").Value("bar").Nx().Build()).Error()
    ...
```
[Full Example Source](/plugin/rueidis/example/rueidis_example.go)
