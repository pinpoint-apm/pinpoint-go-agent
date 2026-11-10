# ppgorm
This package instruments the [go-gorm/gorm](https://github.com/go-gorm/gorm) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorm/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorm/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorm/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorm/v2)

This package instruments the go-gorm/gorm calls. Use the Open as the gorm.Open.

``` go
g, err := ppgorm.Open(mysql.New(mysql.Config{Conn: db}), &gorm.Config{})
```

ppgorm records the GORM call only. The SQL, its bind values and the database node come from the
`database/sql` driver underneath, so give GORM a pool opened through a pinpoint SQL plugin -
`Conn: db` with `db` from `sql.Open("mysql-pinpoint", dsn)` as above, or the driver name:

``` go
g, err := ppgorm.Open(mysql.New(mysql.Config{DriverName: "mysql-pinpoint", DSN: dsn}), &gorm.Config{})
```

The dialectors' defaults, `mysql.Open(dsn)` and `postgres.Open(dsn)`, open the pool through the
uninstrumented driver, and no SQL is recorded. See [SQL databases](/doc/plugin_guide.md#sql-databases).

Or register the callbacks on a `*gorm.DB` opened elsewhere (idempotent, so it
is safe on a db `ppgorm.Open` already instrumented):

``` go
g = ppgorm.Instrument(g)
```

It is necessary to pass the context containing the pinpoint.Tracer to gorm.DB.

``` go
g = g.WithContext(pinpoint.NewContext(context.Background(), tracer))
g.Create(&Product{Code: "D42", Price: 100})
```

``` go
import (
    "database/sql"
    "log"
    "net/http"

    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorm/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
    _ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql/v2"
    "gorm.io/driver/mysql"
    "gorm.io/gorm"
)

type Product struct {
    gorm.Model
    Code  string
    Price uint
}

var gormdb *gorm.DB // opened once in main and shared by the handlers

func gormQuery(w http.ResponseWriter, r *http.Request) {
    // r.Context() carries the tracer the HTTP plugin put there.
    db := gormdb.WithContext(r.Context())

    db.Create(&Product{Code: "D42", Price: 100})

    var product Product
    if err := db.First(&product, "code = ?", "D42").Error; err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    ...
}

func main() {
    // ... create the agent, as in the Getting Started guide

    // parseTime lets gorm.Model's time columns scan into time.Time.
    sqlDB, err := sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/testdb?parseTime=true")
    if err != nil {
        log.Fatal(err)
    }
    defer sqlDB.Close()

    gormdb, err = ppgorm.Open(mysql.New(mysql.Config{Conn: sqlDB}), &gorm.Config{})
    if err != nil {
        log.Fatal(err)
    }
    gormdb.AutoMigrate(&Product{})

    http.HandleFunc("/gorm", pphttp.WrapHandlerFunc(gormQuery))
    log.Fatal(http.ListenAndServe(":8000", nil))
}
```
[Full Example Source](/example/gorm/gorm_example.go)
