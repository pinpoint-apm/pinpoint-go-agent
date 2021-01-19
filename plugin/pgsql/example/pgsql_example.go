package main

import (
	"context"
	"database/sql"
	"fmt"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	_ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgsql"
	"log"
	"net/http"
	"os"
)

func query(w http.ResponseWriter, r *http.Request) {
	db, err := sql.Open("pq-pinpoint", "postgresql://test:test!@localhost/testdb?sslmode=disable")
	if err != nil {
		panic(err)
	}

	tracer := pinpoint.FromContext(r.Context())
	ctx := pinpoint.NewContext(context.Background(), tracer)
	row := db.QueryRowContext(ctx, "SELECT count(*) FROM pg_catalog.pg_tables")
	var count int
	err = row.Scan(&count)
	if err != nil {
		log.Fatalf("sql error: %v", err)
	}

	fmt.Println("number of entries in pg_catalog.pg_tables", count)
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoPgSqlTest"),
		pinpoint.WithAgentId("GoPgSqlTestId"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}

	http.HandleFunc(phttp.WrapHandleFunc(agent, "query", "/query", query))

	http.ListenAndServe(":9000", nil)
	agent.Shutdown()
}
