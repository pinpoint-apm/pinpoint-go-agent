# pppgxv5
This package instruments the [jackc/pgx/v5](https://github.com/jackc/pgx) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgxv5/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgxv5/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgxv5/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgxv5/v2)

This package instruments the jackc/pgx/v5.
Use the NewTracer as the pgx.ConnConfig.Tracer.

``` go
cfg, err := pgx.ParseConfig("postgresql://test:test!@localhost/testdb?sslmode=disable")
cfg.Tracer = pppgxv5.NewTracer()
conn, err := pgx.ConnectConfig(context.Background(), cfg)
```

For a pool, set it on the connection config the pool is built from:

``` go
cfg, err := pgxpool.ParseConfig("postgresql://test:test!@localhost/testdb?sslmode=disable")
cfg.ConnConfig.Tracer = pppgxv5.NewTracer()
pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
```

It is necessary to pass the context containing the pinpoint.Tracer to pgx calls.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
rows := conn.QueryRow(ctx, "SELECT count(*) FROM pg_catalog.pg_tables")
```

A query's event stays open until its `Rows` is closed (for `QueryRow`, until `Scan` returns): it covers the row loop,
and whatever the loop records nests under it. Close every `Rows`; one left open keeps its event open for the rest
of the request, with every later event nested under it.

``` go
import (
    "context"
    "fmt"
    "log"
    "net/http"

    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgxv5/v2"
)

var pool *pgxpool.Pool // opened once in main and shared by the handlers

func tableCount(w http.ResponseWriter, r *http.Request) {
    var count int
    // r.Context() carries the tracer the HTTP plugin put there.
    err := pool.QueryRow(r.Context(), "SELECT count(*) FROM pg_catalog.pg_tables").Scan(&count)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    fmt.Fprintln(w, "number of entries in pg_catalog.pg_tables", count)
}

func main() {
    // ... create the agent, as in the Getting Started guide

    cfg, err := pgxpool.ParseConfig("postgresql://test:test!@localhost/testdb?sslmode=disable")
    if err != nil {
        log.Fatal(err)
    }
    cfg.ConnConfig.Tracer = pppgxv5.NewTracer()
    pool, err = pgxpool.NewWithConfig(context.Background(), cfg)
    if err != nil {
        log.Fatal(err)
    }
    defer pool.Close()

    http.HandleFunc("/tables", pphttp.WrapHandlerFunc(tableCount))
    log.Fatal(http.ListenAndServe(":8000", nil))
}
```

[Full Example Source](/example/pgxv5/pgxv5_example.go)

### database/sql driver
This package instruments the database/sql driver of pgx calls also.
Use this package's driver in place of the pgx driver.

``` go
db, err := sql.Open("pgxv5-pinpoint", "postgresql://test:test!@localhost/testdb?sslmode=disable")
```

It is necessary to pass the context containing the pinpoint.Tracer to all exec and query methods on SQL driver.
Span events are recorded for queries and other statement executions made with the context methods and a context carrying the pinpoint.Tracer.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
row := db.QueryRowContext(ctx, "SELECT count(*) FROM pg_catalog.pg_tables")
```

Use the driver or the native tracer on a connection, not both: a connection opened through `pgxv5-pinpoint`
from a config that also carries `NewTracer()` records every query twice. A name from `stdlib.RegisterConnConfig`
opens through `pgxv5-pinpoint` too; its host and database name are read from each new connection.

A `sql.Conn.Raw` callback is handed the instrumented connection, not pgx's
`*stdlib.Conn`. Unwrap it with `pinpoint.UnwrapSQLConn` before the type
assertion pgx documents, which would otherwise fail and panic; what runs on the
unwrapped connection is not traced.

``` go
err = conn.Raw(func(driverConn any) error {
    pgxConn := pinpoint.UnwrapSQLConn(driverConn).(*stdlib.Conn).Conn()
    ...
})
```
