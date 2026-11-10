# pppgsql
This package instruments the [lib/pq](https://github.com/lib/pq) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgsql/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgsql/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgsql/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgsql/v2)

This package instruments the postgres driver calls.
Use this package's driver in place of the postgres driver.

``` go
db, err := sql.Open("pq-pinpoint", "postgresql://test:test!@localhost/testdb?sslmode=disable")
```

It is necessary to pass the context containing the pinpoint.Tracer to all exec and query methods on SQL driver.
Span events are recorded for queries and other statement executions made with the context methods and a context carrying the pinpoint.Tracer.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
row := db.QueryRowContext(ctx, "SELECT count(*) FROM pg_catalog.pg_tables")
```

``` go
import (
    "database/sql"
    "fmt"
    "log"
    "net/http"

    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
    _ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgsql/v2"
)

var db *sql.DB // opened once in main and shared by the handlers

func query(w http.ResponseWriter, r *http.Request) {
    var count int
    // r.Context() carries the tracer the HTTP plugin put there.
    err := db.QueryRowContext(r.Context(), "SELECT count(*) FROM pg_catalog.pg_tables").Scan(&count)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    fmt.Fprintln(w, "number of entries in pg_catalog.pg_tables", count)
}

func main() {
    // ... create the agent, as in the Getting Started guide

    var err error
    db, err = sql.Open("pq-pinpoint", "postgresql://test:test!@localhost/testdb?sslmode=disable")
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    http.HandleFunc("/query", pphttp.WrapHandlerFunc(query))
    log.Fatal(http.ListenAndServe(":8000", nil))
}
```
[Full Example Source](/example/pgsql/pgsql_example.go)
