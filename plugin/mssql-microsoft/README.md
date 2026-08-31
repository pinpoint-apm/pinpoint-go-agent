# ppmssqlmicrosoft
This package instruments the [microsoft/go-mssqldb](https://github.com/microsoft/go-mssqldb) package.

This is the maintained successor of [denisenkom/go-mssqldb](https://github.com/denisenkom/go-mssqldb),
which is instrumented by [plugin/mssql](/plugin/mssql).
The two register different driver names, so a binary may import both.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/mssql-microsoft
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mssql-microsoft"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/mssql-microsoft)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/mssql-microsoft)

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
    "github.com/pinpoint-apm/pinpoint-go-agent"
    _ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mssql-microsoft"
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
[Full Example Source](/plugin/mssql-microsoft/example/mssql_example.go)
