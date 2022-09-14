package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	_ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql"
)

func tableCount(w http.ResponseWriter, r *http.Request) {
	tracer := pinpoint.FromContext(r.Context())

	db, err := sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/information_schema")
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, err.Error())
		return
	}
	defer db.Close()

	ctx := pinpoint.NewContext(context.Background(), tracer)
	row := db.QueryRowContext(ctx, "SELECT count(*) from tables")
	var count int
	row.Scan(&count)

	fmt.Println("number of tables in information_schema", count)
}

func query(w http.ResponseWriter, r *http.Request) {
	db, err := sql.Open("mysql-pinpoint", "root:p123@tcp(127.0.0.1:3306)/testdb")
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, err.Error())
		return
	}
	defer db.Close()

	ctx := pinpoint.NewContext(context.Background(), pinpoint.TracerFromRequestContext(r))

	res, _ := db.ExecContext(ctx, "CREATE TABLE employee (id INT AUTO_INCREMENT, emp_name VARCHAR(64), department VARCHAR(64), created DATE, PRIMARY KEY (id))")

	stmt, err := db.Prepare("INSERT employee SET emp_name = ?, department = ?, created = ?")
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, err.Error())
		return
	}

	res, _ = stmt.ExecContext(ctx, "foo", "pinpoint", "2022-08-15")
	res, _ = stmt.ExecContext(ctx, "bar", "avengers", "2022-08-16")
	id, _ := res.LastInsertId()
	fmt.Println("Insert ID", id)
	stmt.Close()

	stmt, err = db.PrepareContext(ctx, "UPDATE employee SET emp_name = ? where id = ?")
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, err.Error())
		return
	}

	//not traced
	res, _ = stmt.Exec("ironman", id)
	_, _ = res.RowsAffected()
	stmt.Close()

	var (
		uid        int
		empName    string
		department string
		created    string
	)

	rows, _ := db.QueryContext(ctx, "SELECT * FROM employee")
	for rows.Next() {
		_ = rows.Scan(&uid, &empName, &department, &created)
		fmt.Printf("user: %d, %s, %s, %s\n", uid, empName, department, created)
	}
	rows.Close()

	//not traced
	rows, _ = db.Query("SELECT * FROM employee WHERE id = 1")
	rows.Close()

	stmt, err = db.Prepare("SELECT * FROM employee WHERE id = ?")
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, err.Error())
		return
	}

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

	_, err = tx.ExecContext(ctx, "INSERT INTO employee(emp_name, department, created) VALUES ('ipad', 'apple', '2022-08-15'), ('chrome', 'google', '2022-08-18')")
	if err != nil {
		tx.Rollback()
		return
	}

	row := tx.QueryRowContext(ctx, "SELECT count(*) FROM employee")
	var count int
	err = row.Scan(&count)
	if err != nil {
		tx.Rollback()
		return
	}

	_, err = tx.ExecContext(ctx, "UPDATE employee SET emp_name = 'macbook' WHERE id = ?", 3)
	if err != nil {
		tx.Rollback()
		return
	}

	err = tx.Commit()
	if err != nil {
		log.Fatal(err)
	}

}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoMySQLTest"),
		pinpoint.WithAgentId("GoMySQLTestId"),
		//pinpoint.WithSamplingType("PERCENT"),
		//pinpoint.WithSamplingPercentRate(10),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	http.HandleFunc(phttp.WrapHandleFunc("/tableCount", tableCount))
	http.HandleFunc(phttp.WrapHandleFunc("/query", query))

	http.ListenAndServe(":9001", nil)
}
