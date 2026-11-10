package main

import (
	"database/sql"
	"log"
	"net/http"
	"os"

	ppgorm "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gorm/v2"
	pphttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
	_ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type Product struct {
	gorm.Model
	Code  string
	Price uint
}

var db *sql.DB // opened once in main, shared by every request

func gormQuery(w http.ResponseWriter, r *http.Request) {
	gormdb, err := ppgorm.Open(mysql.New(mysql.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context() // carries the request's tracer; no need to rewrap it
	gormdb = gormdb.WithContext(ctx)

	if err := gormdb.AutoMigrate(&Product{}); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Create. Every request works on the row it creates, by the ID gorm
	// filled in: a fixed ID 1 is soft-deleted by the first request, so later
	// ones would miss it and leave their own rows behind.
	product := Product{Code: "D42", Price: 100}
	if err := gormdb.Create(&product).Error; err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Read
	gormdb.First(&product, product.ID)
	gormdb.First(&product, "id = ? AND code = ?", product.ID, "D42")

	// Update - update product's price to 200
	gormdb.Model(&product).Update("Price", 200)

	// Delete - delete product
	gormdb.Delete(&product)
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoGormTest"),
		pinpoint.WithUidVersion("v3"),
		pinpoint.WithAgentName("GoGormTestId"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()
	defer pinpoint.ShutdownOnSignal(agent)() // SIGTERM, SIGINT

	db, err = sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/testdb?parseTime=true")
	if err != nil {
		log.Fatalf("cannot open database: %v", err)
	}
	defer db.Close()

	http.HandleFunc("/gormquery", pphttp.WrapHandlerFunc(gormQuery))

	http.ListenAndServe(":9019", nil)
}
