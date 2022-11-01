package main

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	_ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mssql"
)

func query(w http.ResponseWriter, r *http.Request) {
	dsn := "server=localhost;user id=sa;password=TestPass123;port=1433;database=TestDB"
	db, err := sql.Open("sqlserver-pinpoint", dsn)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, err.Error())
		return
	}
	defer db.Close()

	ctx := r.Context()
	_, _ = db.ExecContext(ctx, "CREATE TABLE Inventory (id INT, name NVARCHAR(50), quantity INT)")

	stmt, err := db.Prepare("INSERT INTO Inventory VALUES (@p1, @p2, @p3)")
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, err.Error())
		return
	}

	_, _ = stmt.ExecContext(ctx, 1, "foo", 100)
	_, _ = stmt.ExecContext(ctx, 2, "bar", 200)
	stmt.Close()

	var (
		id       int
		name     string
		quantity int
	)

	rows, _ := db.QueryContext(ctx, "SELECT * FROM Inventory")
	for rows.Next() {
		_ = rows.Scan(&id, &name, &quantity)
		fmt.Printf("user: %d, %s, %d\n", id, name, quantity)
	}
	rows.Close()
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoMsSQLTest"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	http.HandleFunc("/query", pphttp.WrapHandlerFunc(query))

	http.ListenAndServe(":9000", nil)
}
