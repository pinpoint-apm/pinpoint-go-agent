# ppgorm
This package instruments the [go-gorm/gorm](https://github.com/go-gorm/gorm) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorm
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorm"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorm)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorm)

This package instruments the go-gorm/gorm calls. Use the Open as the gorm.Open.

``` go
g, err := ppgorm.Open(mysql.New(mysql.Config{Conn: db}), &gorm.Config{})
```

It is necessary to pass the context containing the pinpoint.Tracer to gorm.DB.

``` go
g = g.WithContext(pinpoint.NewContext(context.Background(), tracer))
g.Create(&Product{Code: "D42", Price: 100})
```

``` go
package main

import (
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorm"
    _ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql"
    "gorm.io/driver/mysql"
    "gorm.io/gorm"
)

func gormQuery(w http.ResponseWriter, r *http.Request) {
    db, err := sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/testdb")
    gormdb, err := ppgorm.Open(mysql.New(mysql.Config{Conn: db}), &gorm.Config{})
    gormdb = gormdb.WithContext(r.Context())

    gormdb.AutoMigrate(&Product{})
    gormdb.Create(&Product{Code: "D42", Price: 100})

    var product Product
    gormdb.First(&product, "code = ?", "D42")

    ...
}
```
[Full Example Source](/plugin/gorm/example/gorm_example.go)
