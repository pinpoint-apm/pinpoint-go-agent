# pporaclev3
This package instruments the [sijms/go-ora/v3](https://github.com/sijms/go-ora) package.

> **Note**
> This plugin cannot be used in the same binary as [plugin/oracle](/plugin/oracle). go-ora v2
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

> **Note**
> go-ora v3.0.1 fails every connection after the first with `EOF` unless the
> DSN turns fast login off with `FAST LOGIN=false` (`?FAST%20LOGIN=false` in a
> URL DSN). The first connection stores a fast login cookie in a package-wide
> store, every later connection - a new `sql.DB` or the pool growing - logs in
> with it, and Oracle 23 closes the session. The error is not
> `driver.ErrBadConn`, so go-ora does not drop the cookie and retry. The plain
> go-ora v3 driver fails the same way; v2 has no fast login and is not affected.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/oraclev3/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/oraclev3/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/oraclev3/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/oraclev3/v2)

This package instruments the Oracle driver calls.
Use this package's driver in place of the Oracle driver.

``` go
db, err := sql.Open("oraclev3-pinpoint", "oracle://scott:tiger@localhost:1521/xe?FAST%20LOGIN=false")
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
    _ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/oraclev3/v2"
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
    db, err = sql.Open("oraclev3-pinpoint", "oracle://scott:tiger@localhost:1521/xe?FAST%20LOGIN=false")
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    http.HandleFunc("/query", pphttp.WrapHandlerFunc(query))
    log.Fatal(http.ListenAndServe(":8000", nil))
}
```
[Full Example Source](/example/oraclev3/oraclev3_example.go)
