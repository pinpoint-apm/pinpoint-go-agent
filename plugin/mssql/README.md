# ppmssql
This package instruments the [denisenkom/go-mssqldb](https://github.com/denisenkom/go-mssqldb) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/mssql
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mssql"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/mssql)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/mssql)

This package instruments the MS SQL Server driver calls.
Use this package's driver in place of the SQL Server driver.

``` go
dsn := "server=localhost;user id=sa;password=TestPass123;port=1433;database=TestDB"
db, err := sql.Open("sqlserver-pinpoint", dsn)
```

It is necessary to pass the context containing the pinpoint.Tracer to all exec and query methods on SQL driver.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
row, err := db.QueryContext(ctx, "SELECT * FROM Inventory")
```

``` go
import (
    "database/sql"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    _ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mssql"
)

func query(w http.ResponseWriter, r *http.Request) {
    dsn := "server=localhost;user id=sa;password=TestPass123;port=1433;database=TestDB"
    db, err := sql.Open("sqlserver-pinpoint", dsn)
    defer db.Close()

    rows, _ := db.QueryContext(r.Context(), "SELECT * FROM Inventory")
    for rows.Next() {
        _ = rows.Scan(&id, &name, &quantity)
        fmt.Printf("user: %d, %s, %d\n", id, name, quantity)
    }
    rows.Close()
}
```
[Full Example Source](/plugin/mssql/example/mssql_example.go)
