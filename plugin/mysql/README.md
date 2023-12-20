# ppmysql
This package instruments the [go-sql-driver/mysql](https://github.com/go-sql-driver/mysql) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql)

This package instruments the mysql driver calls.
Use this package's driver in place of the mysql driver.

``` go
db, err := sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/information_schema")
```

It is necessary to pass the context containing the pinpoint.Tracer to all exec and query methods on SQL driver.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
row := db.QueryRowContext(ctx, "SELECT count(*) from tables")
```

``` go
import (
    "database/sql"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    _ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql"
)

func query(w http.ResponseWriter, r *http.Request) {
    db, err := sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/information_schema")
    row := db.QueryRowContext(r.Context(), "SELECT count(*) from tables")

    var count int
    row.Scan(&count)
    fmt.Println("number of tables in information_schema", count)
}
```
[Full Example Source](/plugin/mysql/example/mysql_example.go)
