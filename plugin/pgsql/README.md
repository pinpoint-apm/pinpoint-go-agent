# pppgsql
This package instruments the [lib/pq](https://github.com/lib/pq) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgsql
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgsql"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgsql)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgsql)

This package instruments the postgres driver calls.
Use this package's driver in place of the postgres driver.

``` go
db, err := sql.Open("pq-pinpoint", "postgresql://test:test!@localhost/testdb?sslmode=disable")
```

It is necessary to pass the context containing the pinpoint.Tracer to all exec and query methods on SQL driver.
Spans will be created for queries and other statement executions if the context methods are used, and the context includes a transaction.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
row := db.QueryRowContext(ctx, "SELECT count(*) FROM pg_catalog.pg_tables")
```

``` go
import (
    pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
    _ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgsql"
)

func query(w http.ResponseWriter, r *http.Request) {
    db, err := sql.Open("pq-pinpoint", "postgresql://test:test!@localhost/testdb?sslmode=disable")
    row := db.QueryRowContext(r.Context(), "SELECT count(*) FROM pg_catalog.pg_tables")

    var count int
    err = row.Scan(&count)
    fmt.Println("number of entries in pg_catalog.pg_tables", count)
}
```
[Full Example Source](/plugin/pgsql/example/pgsql_example.go)
