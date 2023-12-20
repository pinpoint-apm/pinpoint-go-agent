# ppgohbase
This package instruments the [tsuna/gohbase](https://github.com/tsuna/gohbase) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/gohbase
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gohbase"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/gohbase)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/gohbase)

This package instruments the gohbase calls. Use the NewClient as the gohbase.NewClient.

``` go
client := ppgohbase.NewClient("localhost")
```

It is necessary to pass the context containing the pinpoint.Tracer to gohbase.Client.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
putRequest, _ := hrpc.NewPutStr(ctx, "table", "key", values)
client.Put(putRequest)
```

``` go
import (
    "github.com/tsuna/gohbase/hrpc"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gohbase"
)

func doHbase(w http.ResponseWriter, r *http.Request) {
    client := ppgohbase.NewClient("localhost")

    values := map[string]map[string][]byte{"cf": {"a": []byte{0}}}
    putRequest, err := hrpc.NewPutStr(r.Context(), "table", "key", values)
    _, err = client.Put(putRequest)
	
    ...
}
```
[Full Example Source](/plugin/gohbase/example/hbase_example.go)
