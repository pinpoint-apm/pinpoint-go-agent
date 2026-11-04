# ppmssqlmicrosoft
This package instruments the [microsoft/go-mssqldb](https://github.com/microsoft/go-mssqldb) package.

The driver behaves like go-mssqldb's `sqlserver` driver name, not the deprecated `mssql`: the query text goes to
the server as written, so parameters are `@p1` or `@name`, and the `?`, `$1` and `:1` placeholders `mssql` rewrites
are a syntax error. With sqlx, register the name: `sqlx.BindDriver("mssql-microsoft-pinpoint", sqlx.AT)`.
go-mssqldb's `SetLogger` and `SetContextLogger` configure its own two driver instances only, so connections
opened through this plugin do not log through them.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/mssql-microsoft/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mssql-microsoft/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/mssql-microsoft/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/mssql-microsoft/v2)

This package instruments the MS SQL Server driver calls.
Use this package's driver in place of the SQL Server driver.

``` go
dsn := "server=localhost;user id=sa;password=TestPass123;port=1433;database=TestDB"
db, err := sql.Open("mssql-microsoft-pinpoint", dsn)
```

It is necessary to pass the context containing the pinpoint.Tracer to all exec and query methods on SQL driver.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
row, err := db.QueryContext(ctx, "SELECT * FROM Inventory")
```

``` go
import (
    "database/sql"
    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    _ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mssql-microsoft/v2"
)

func query(w http.ResponseWriter, r *http.Request) {
    dsn := "server=localhost;user id=sa;password=TestPass123;port=1433;database=TestDB"
    db, err := sql.Open("mssql-microsoft-pinpoint", dsn)
    defer db.Close()

    rows, _ := db.QueryContext(r.Context(), "SELECT * FROM Inventory")
    for rows.Next() {
        _ = rows.Scan(&id, &name, &quantity)
        fmt.Printf("user: %d, %s, %d\n", id, name, quantity)
    }
    rows.Close()
}
```
[Full Example Source](/example/mssql-microsoft/mssql_example.go)
