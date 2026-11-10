# pporacle
This package instruments the [sijms/go-ora/v2](https://github.com/sijms/go-ora) package.

> **Note**
> This plugin cannot be used in the same binary as [plugin/oraclev3](/plugin/oraclev3). go-ora v2
> and v3 both call `sql.Register("oracle", ...)` in their own package `init`, so
> linking both panics at startup on a duplicate driver name:
>
> ```
> panic: sql: Register called twice for driver oracle
> ```
>
> This is upstream's doing and neither plugin can work around it - the driver
> names the plugins themselves register (`oracle-pinpoint`, `oraclev3-pinpoint`)
> are already distinct. Pick one go-ora major per binary.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/oracle/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/oracle/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/oracle/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/oracle/v2)

This package instruments the Oracle driver calls.
Use this package's driver in place of the Oracle driver.

``` go
db, err := sql.Open("oracle-pinpoint", "oracle://scott:tiger@localhost:1521/xe")
```

It is necessary to pass the context containing the pinpoint.Tracer to all exec and query methods on SQL driver.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
row, err := db.QueryContext(ctx, "SELECT * FROM BONUS")
```

``` go
import (
    "database/sql"
    "fmt"
    "log"
    "net/http"

    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
    _ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/oracle/v2"
)

var db *sql.DB // opened once in main and shared by the handlers

func query(w http.ResponseWriter, r *http.Request) {
    // r.Context() carries the tracer the HTTP plugin put there.
    rows, err := db.QueryContext(r.Context(), "SELECT ENAME, JOB, SAL, COMM FROM BONUS")
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    defer rows.Close()

    for rows.Next() {
        var (
            ename, job string
            sal, comm  float32
        )
        if err := rows.Scan(&ename, &job, &sal, &comm); err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        fmt.Fprintln(w, "ENAME:", ename, "JOB:", job, "SAL:", sal, "COMM:", comm)
    }
}

func main() {
    // ... create the agent, as in the Getting Started guide

    var err error
    db, err = sql.Open("oracle-pinpoint", "oracle://scott:tiger@localhost:1521/xe")
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    http.HandleFunc("/query", pphttp.WrapHandlerFunc(query))
    log.Fatal(http.ListenAndServe(":8000", nil))
}
```
[Full Example Source](/example/oracle/oracle_example.go)
