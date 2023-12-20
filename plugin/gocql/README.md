# ppgocql
This package instruments the [gocql](https://github.com/gocql/gocql) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/gocql
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gocql"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/gocql)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/gocql)

This package instruments all queries created from gocql session.
Use the NewObserver as the gocql.QueryObserver or gocql.BatchObserver:

``` go
cluster := gocql.NewCluster("127.0.0.1")

observer := ppgocql.NewObserver()
cluster.QueryObserver = observer
cluster.BatchObserver = observer
```

It is necessary to pass the context containing the pinpoint.Tracer using the pinpoint.WithContext function.

``` go
import (
    "github.com/gocql/gocql"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gocql"
)

func doCassandra(w http.ResponseWriter, r *http.Request) {
    observer := ppgocql.NewObserver()
    cluster := gocql.NewCluster("127.0.0.1")
    cluster.QueryObserver = observer
    cluster.BatchObserver = observer

    session, _ := cluster.CreateSession()
    query := session.Query(`SELECT id, text FROM tweet WHERE timeline = ? LIMIT 1`, "me")
    err := query.WithContext(r.Context()).Consistency(gocql.One).Scan(&id, &text)
    ...
}

```
[Full Example Source](/plugin/gocql/example/gocql_example.go)
