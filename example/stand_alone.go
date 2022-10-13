package main

import (
	"context"
	"database/sql"
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	_ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql"
)

func newSpan(name string) pinpoint.Tracer {
	return pinpoint.GetAgent().NewSpanTracer(name, "/")
}

func query(ctx context.Context) {
	tracer := pinpoint.FromContext(ctx)
	defer tracer.NewSpanEvent("query").EndSpanEvent()

	db, err := sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/information_schema")
	if nil != err {
		panic(err)
	}

	gormdb, err := gorm.Open(mysql.New(mysql.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		panic("failed to connect database")
	}

	gormdb = gormdb.WithContext(ctx)
	row := gormdb.Raw("SELECT count(*) from tables")

	var count int
	row.Scan(&count)
	fmt.Println("number of tables in information_schema", count)
}

func outGoing(ctx context.Context) {
	tracer := pinpoint.FromContext(ctx)
	defer tracer.NewSpanEvent("outGoing").EndSpanEvent()

	client := pphttp.WrapClient(nil)

	request, _ := http.NewRequest("GET", "https://github.com/pinpoint-apm/pinpoint-go-agent/blob/main/README.md", nil)
	request = request.WithContext(ctx)

	resp, err := client.Do(request)
	if nil != err {
		fmt.Printf("error: %s\n", err.Error())
		return
	}

	fmt.Println("http request success")
	resp.Body.Close()
}

func run() {
	tracer := newSpan("SaAppTest")
	defer tracer.EndSpan()
	defer tracer.NewSpanEvent("run").EndSpanEvent()

	ctx := pinpoint.NewContext(context.Background(), tracer)
	query(ctx)
	outGoing(ctx)
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoSaAppTest"),
		pinpoint.WithAgentId("GoSaAppTestId"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	for true {
		run()
		time.Sleep(3 * time.Second)
	}
}
