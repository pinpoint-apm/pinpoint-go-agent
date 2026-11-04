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
    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    _ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/oraclev3/v2"
)

func query(w http.ResponseWriter, r *http.Request) {
    conn, err := sql.Open("oraclev3-pinpoint", "oracle://scott:tiger@localhost:1521/xe?FAST%20LOGIN=false")
    rows, _ := conn.QueryContext(r.Context(), "SELECT * FROM BONUS")

    for rows.Next() {
        err = rows.Scan(&ename, &job, &sal, &comm)
        fmt.Println("ENAME: ", ename, "\tJOB: ", job, "\tSAL: ", sal, "\tCOMM: ", comm)
    }
}
```
[Full Example Source](/example/oraclev3/oraclev3_example.go)
