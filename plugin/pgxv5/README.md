# pppgxv5
This package instruments the [jackc/pgx/v5](https://github.com/jackc/pgx) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgxv5
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgxv5"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgxv5)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgxv5)

This package instruments the jackc/pgx/v5.
Use the NewTracer as the pgx.ConnConfig.Tracer.

``` go
cfg, err := pgx.ParseConfig("postgresql://test:test!@localhost/testdb?sslmode=disable")
cfg.Tracer = pppgxv5.NewTracer()
conn, err := pgx.ConnectConfig(context.Background(), cfg)
```

It is necessary to pass the context containing the pinpoint.Tracer to pgx calls.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
rows := conn.QueryRow(ctx, "SELECT count(*) FROM pg_catalog.pg_tables")
```

``` go
import (
    "github.com/jackc/pgx/v5"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgxv5"
)

func connect() *pgx.Conn {
    cfg, err := pgx.ParseConfig(connUrl)
    cfg.Tracer = pppgxv5.NewTracer()
    conn, err := pgx.ConnectConfig(context.Background(), cfg)
    return conn
}

func tableCount(w http.ResponseWriter, r *http.Request) {
    dbConn := connect()
    defer dbConn.Close(context.Background())

    tracer := pinpoint.FromContext(r.Context())
    ctx := pinpoint.NewContext(context.Background(), tracer)

    rows := dbConn.QueryRow(ctx, "SELECT count(*) FROM pg_catalog.pg_tables")
    ...
}
```

[Full Example Source](/plugin/pgxv5/example/pgxv5_example.go)

### database/sql driver
This package instruments the database/sql driver of pgx calls also.
Use this package's driver in place of the pgx driver.

``` go
db, err := sql.Open("pgxv5-pinpoint", "postgresql://test:test!@localhost/testdb?sslmode=disable")
```

It is necessary to pass the context containing the pinpoint.Tracer to all exec and query methods on SQL driver.
Spans will be created for queries and other statement executions if the context methods are used, and the context includes a transaction.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
row := db.QueryRowContext(ctx, "SELECT count(*) FROM pg_catalog.pg_tables")
```
