# ppgoelasticv9
This package instruments the [elastic/go-elasticsearch/v9](https://github.com/elastic/go-elasticsearch) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/goelasticv9
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goelasticv9"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/goelasticv9)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/goelasticv9)

This package instruments the go-elasticsearch/v9 calls.
Use the NewTransport function as the elasticsearch.Client's Transport.

``` go
es, err := elasticsearch.NewClient(
    elasticsearch.Config{
        Transport: ppgoelasticv9.NewTransport(nil),
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
    "github.com/elastic/go-elasticsearch/v9"
    "github.com/elastic/go-elasticsearch/v9/esapi"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goelasticv9"
)

func goelasticv9(w http.ResponseWriter, req *http.Request) {
    ctx := req.Context()
    es, err := elasticsearch.NewClient(
        elasticsearch.Config{Transport: ppgoelasticv9.NewTransport(nil)}
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
[Full Example Source](/plugin/goelasticv9/example/goelasticv9.go)
