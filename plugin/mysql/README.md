# ppmysql
This package instruments the [go-sql-driver/mysql](https://github.com/go-sql-driver/mysql) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql/v2)

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
    "fmt"
    "log"
    "net/http"

    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
    _ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql/v2"
)

var db *sql.DB // opened once in main and shared by the handlers

func query(w http.ResponseWriter, r *http.Request) {
    var count int
    // r.Context() carries the tracer the HTTP plugin put there.
    err := db.QueryRowContext(r.Context(), "SELECT count(*) from tables").Scan(&count)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    fmt.Fprintln(w, "number of tables in information_schema", count)
}

func main() {
    // ... create the agent, as in the Getting Started guide

    var err error
    db, err = sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/information_schema")
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    http.HandleFunc("/query", pphttp.WrapHandlerFunc(query))
    log.Fatal(http.ListenAndServe(":8000", nil))
}
```
[Full Example Source](/example/mysql/mysql_example.go)
