# ppredigo
This package instruments the [gomodule/redigo](https://github.com/gomodule/redigo) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/redigo
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/redigo"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/redigo)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/redigo)

This package instruments the gomodule/redigo calls.
Use the Dial, DialContext (or DialURL, DialURLContext) as the redis.Dial.

``` go
c, err := ppredigo.Dial("tcp", "127.0.0.1:6379")
```

It is necessary to propagate the context that contains the pinpoint.Tracer to redis.Conn.
You can call WithContext to propagate a context containing a pinpoint.Tracer to the operations:
``` go
ppredigo.WithContext(c, pinpoint.NewContext(context.Background(), tracer))
c.Do("SET", "vehicle", "truck")
```

Also, you can use function taking the context like redis.DoContext.

``` go
redis.DoContext(c, pinpoint.NewContext(context.Background(), tracer), "GET", "vehicle")
```

``` go
package main

import (
    "github.com/gomodule/redigo/redis"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/redigo"
)

func redigo_test(w http.ResponseWriter, r *http.Request) {
    c, err := ppredigo.Dial("tcp", "127.0.0.1:6379")
    ppredigo.WithContext(c, r.Context())

    c.Do("SET", "vehicle", "truck")
    redis.DoWithTimeout(c, 1000*time.Millisecond, "GET", "vehicle")
    
    //or 
    //redis.DoContext(c, r.Context(), "GET", "vehicle")
```
[Full Example Source](/plugin/redigo/example/redigo_example.go)
