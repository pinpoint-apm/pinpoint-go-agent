# ppgocqlv2
This package instruments the [gocql/v2](https://github.com/apache/cassandra-gocql-driver) package.
The v2 driver is published under the module path `github.com/apache/cassandra-gocql-driver/v2`.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/gocqlv2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gocqlv2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/gocqlv2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/gocqlv2)

This package instruments all queries created from gocql session.
Use the NewObserver as the gocql.QueryObserver or gocql.BatchObserver:

``` go
cluster := gocql.NewCluster("127.0.0.1")

observer := ppgocqlv2.NewObserver()
cluster.QueryObserver = observer
cluster.BatchObserver = observer
```

It is necessary to pass the context containing the pinpoint.Tracer using the pinpoint.WithContext function.

``` go
import (
    "github.com/apache/cassandra-gocql-driver/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gocqlv2"
)

func doCassandra(w http.ResponseWriter, r *http.Request) {
    observer := ppgocqlv2.NewObserver()
    cluster := gocql.NewCluster("127.0.0.1")
    cluster.QueryObserver = observer
    cluster.BatchObserver = observer

    session, _ := cluster.CreateSession()
    query := session.Query(`SELECT id, text FROM tweet WHERE timeline = ? LIMIT 1`, "me")
    err := query.WithContext(r.Context()).Consistency(gocql.One).Scan(&id, &text)
    ...
}

```
[Full Example Source](/plugin/gocqlv2/example/gocqlv2_example.go)
