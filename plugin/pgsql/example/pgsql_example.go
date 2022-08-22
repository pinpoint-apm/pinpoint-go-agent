package main

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	_ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/pgsql"
	"log"
	"net/http"
	"os"
)

func tableCount(w http.ResponseWriter, r *http.Request) {
	db, err := sql.Open("pq-pinpoint", "postgresql://testuser:p123@localhost/testdb?sslmode=disable")
	if err != nil {
		panic(err)
	}
	defer db.Close()

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

func query(w http.ResponseWriter, r *http.Request) {
	db, err := sql.Open("pq-pinpoint", "postgresql://testuser:p123@localhost/testdb?sslmode=disable")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	ctx := pinpoint.NewContext(context.Background(), pinpoint.TracerFromRequestContext(r))

	_, _ = db.ExecContext(ctx, "CREATE TABLE employee (id INTEGER PRIMARY KEY, emp_name VARCHAR(64), department VARCHAR(64), created DATE)")

	stmt, err := db.Prepare("INSERT INTO employee VALUES ($1, $2, $3, $4)")
	if err != nil {
		panic(err)
	}

	_, _ = stmt.ExecContext(ctx, 1, "foo", "pinpoint", "2022-08-15")
	_, _ = stmt.ExecContext(ctx, 2, "bar", "avengers", "2022-08-16")
	stmt.Close()

	stmt, err = db.PrepareContext(ctx, "UPDATE employee SET emp_name = $1 where id = $2")
	if err != nil {
		panic(err)
	}

	res, _ := stmt.ExecContext(ctx, "ironman", 2)
	_, _ = res.RowsAffected()
	stmt.Close()

	var (
		uid        int
		empName    string
		department string
		created    string
	)

	rows, _ := db.QueryContext(ctx, "SELECT * FROM employee WHERE id = 1")
	for rows.Next() {
		_ = rows.Scan(&uid, &empName, &department, &created)
		fmt.Printf("user: %d, %s, %s, %s\n", uid, empName, department, created)
	}
	rows.Close()

	//not traced
	rows, _ = db.Query("SELECT * FROM employee WHERE id = 2")
	rows.Close()

	stmt, _ = db.Prepare("SELECT * FROM employee WHERE id = $1")
	rows, _ = stmt.QueryContext(ctx, 1)
	for rows.Next() {
		_ = rows.Scan(&uid, &empName, &department, &created)
		fmt.Printf("user: %d, %s, %s, %s\n", uid, empName, department, created)
	}
	rows.Close()
	stmt.Close()

	tx(ctx, db)

	res, _ = db.ExecContext(ctx, "DROP TABLE employee")
}

func tx(ctx context.Context, db *sql.DB) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}

	_, err = tx.ExecContext(ctx, "INSERT INTO employee VALUES (3, 'ipad', 'apple', '2022-08-15'), ($1, $2, $3, $4)",
		4, "chrome", "google", "2022-08-18")
	if err != nil {
		tx.Rollback()
		return
	}

	// Run a query to get a count of all cats
	row := tx.QueryRowContext(ctx, "SELECT count(*) FROM employee")
	var catCount int
	// Store the count in the `catCount` variable
	err = row.Scan(&catCount)
	if err != nil {
		tx.Rollback()
		return
	}

	// Now update the food table, increasing the quantity of cat food by 10x the number of cats
	_, err = tx.ExecContext(ctx, "UPDATE employee SET emp_name = 'macbook' WHERE id = $1", 3)
	if err != nil {
		tx.Rollback()
		return
	}

	// Commit the change if all queries ran successfully
	err = tx.Commit()
	if err != nil {
		log.Fatal(err)
	}

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

	http.HandleFunc(phttp.WrapHandleFunc(agent, "tableCount", "/tableCount", tableCount))
	http.HandleFunc(phttp.WrapHandleFunc(agent, "query", "/query", query))

	http.ListenAndServe(":9002", nil)
	agent.Shutdown()
}
