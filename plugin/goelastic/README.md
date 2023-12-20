# ppgoelastic
This package instruments the [elastic/go-elasticsearch](https://github.com/elastic/go-elasticsearch) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/goelastic
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goelastic"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/goelastic)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/goelastic)

This package instruments the elasticsearch calls.
Use the NewTransport function as the elasticsearch.Client's Transport.

``` go
es, err := elasticsearch.NewClient(
    elasticsearch.Config{
        Transport: ppgoelastic.NewTransport(nil),
})
```

It is necessary to pass the context containing the pinpoint.Tracer to elasticsearch.Client.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
res, err = es.Search(
    es.Search.WithContext(ctx),
    es.Search.WithIndex("test"),
    ...
)
```

``` go
import (
    "github.com/elastic/go-elasticsearch/v8"
    "github.com/elastic/go-elasticsearch/v8/esapi"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goelastic"
)

func goelastic(w http.ResponseWriter, req *http.Request) {
    ctx := req.Context()
    es, err := elasticsearch.NewClient(
        elasticsearch.Config{Transport: ppgoelastic.NewTransport(nil)}
    )

    ...
    
    res, err = es.Search(
        es.Search.WithContext(ctx),
        es.Search.WithIndex("test"),
        es.Search.WithBody(&buf),
    )

    ...
}
```
[Full Example Source](/plugin/goelastic/example/goelastic.go)
