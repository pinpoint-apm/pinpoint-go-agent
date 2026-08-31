# pporaclev3
This package instruments the [sijms/go-ora/v3](https://github.com/sijms/go-ora) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/oraclev3
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/oraclev3"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/oraclev3)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/oraclev3)

This package instruments the Oracle driver calls.
Use this package's driver in place of the Oracle driver.

``` go
db, err := sql.Open("oraclev3-pinpoint", "oracle://scott:tiger@localhost:1521/xe")
```

It is necessary to pass the context containing the pinpoint.Tracer to all exec and query methods on SQL driver.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
row, err := db.QueryContext(ctx, "SELECT * FROM BONUS")
```

``` go
import (
    "database/sql"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    _ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/oraclev3"
)

func query(w http.ResponseWriter, r *http.Request) {
    conn, err := sql.Open("oraclev3-pinpoint", "oracle://scott:tiger@localhost:1521/xe")
    rows, _ := conn.QueryContext(r.Context(), "SELECT * FROM BONUS")

    for rows.Next() {
        err = rows.Scan(&ename, &job, &sal, &comm)
        fmt.Println("ENAME: ", ename, "\tJOB: ", job, "\tSAL: ", sal, "\tCOMM: ", comm)
    }
}
```
[Full Example Source](/plugin/oraclev3/example/oraclev3_example.go)
