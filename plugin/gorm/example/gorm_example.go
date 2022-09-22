package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	pgorm "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorm"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	_ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type Product struct {
	gorm.Model
	Code  string
	Price uint
}

func gormQuery(w http.ResponseWriter, r *http.Request) {
	db, err := sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/testdb")
	if nil != err {
		panic(err)
	}

	gormdb, err := pgorm.Open(mysql.New(mysql.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		panic("failed to connect database")
	}

	tracer := pinpoint.FromContext(r.Context())
	ctx := pinpoint.NewContext(context.Background(), tracer)
	gormdb = gormdb.WithContext(ctx)

	gormdb.AutoMigrate(&Product{})

	// Create
	gormdb.Create(&Product{Code: "D42", Price: 100})

	// Read
	var product Product
	gormdb.First(&product, 1)
	gormdb.First(&product, "code = ?", "D42")

	// Update - update product's price to 200
	gormdb.Model(&product).Update("Price", 200)

	// Delete - delete product
	gormdb.Delete(&product, 1)
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoGormTest"),
		pinpoint.WithAgentId("GoGormTestId"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	http.HandleFunc("/gormquery", phttp.WrapHandlerFunc(gormQuery))

	http.ListenAndServe(":9000", nil)
}
