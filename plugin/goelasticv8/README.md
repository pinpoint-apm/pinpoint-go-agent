# ppgoelasticv8
This package instruments the [elastic/go-elasticsearch/v8](https://github.com/elastic/go-elasticsearch) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/goelasticv8
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goelasticv8"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/goelasticv8)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/goelasticv8)

This package instruments the go-elasticsearch/v8 calls.
Use the NewTransport function as the elasticsearch.Client's Transport.

``` go
es, err := elasticsearch.NewClient(
    elasticsearch.Config{
        Transport: ppgoelasticv8.NewTransport(nil),
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
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goelasticv8"
)

func goelasticv8(w http.ResponseWriter, req *http.Request) {
    ctx := req.Context()
    es, err := elasticsearch.NewClient(
        elasticsearch.Config{Transport: ppgoelasticv8.NewTransport(nil)}
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
[Full Example Source](/plugin/goelasticv8/example/goelasticv8.go)
