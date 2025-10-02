package main

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	pphttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	_ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mssql"
)

func query(w http.ResponseWriter, r *http.Request) {
	// First connect to master database to create TestDB if it doesn't exist
	masterDsn := "server=localhost;user id=sa;password=TestPass123;port=1433;database=master"
	masterDb, err := sql.Open("sqlserver-pinpoint", masterDsn)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, err.Error())
		return
	}

	ctx := r.Context()
	// Create TestDB if it doesn't exist
	_, _ = masterDb.ExecContext(ctx, "IF NOT EXISTS (SELECT * FROM sys.databases WHERE name = 'TestDB') CREATE DATABASE TestDB")
	masterDb.Close()

	// Now connect to TestDB
	dsn := "server=localhost;user id=sa;password=TestPass123;port=1433;database=TestDB"
	db, err := sql.Open("sqlserver-pinpoint", dsn)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, err.Error())
		return
	}
	defer db.Close()

	// Drop table if exists and create new one
	_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS Inventory")
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

	http.ListenAndServe(":9021", nil)
}
