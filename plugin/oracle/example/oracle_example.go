package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	_ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/oracle"
)

func insertData(ctx context.Context, conn *sql.DB) error {
	stmt, err := conn.PrepareContext(ctx, "INSERT INTO BONUS(ENAME, JOB, SAL, COMM) VALUES(:1, :2, :3, :4)")
	if err != nil {
		return err
	}
	defer func() {
		_ = stmt.Close()
	}()

	_, err = stmt.ExecContext(ctx, "foo", "developer", 1.1, 2.2)
	_, err = stmt.ExecContext(ctx, "bar", "manager", 3.3, 4.4)
	return err
}

func queryData(ctx context.Context, conn *sql.DB) error {
	rows, err := conn.QueryContext(ctx, "SELECT * FROM BONUS")
	if err != nil {
		return err
	}
	defer func() {
		rows.Close()
	}()
	var (
		ename string
		job   string
		sal   float32
		comm  float32
	)
	for rows.Next() {
		err = rows.Scan(&ename, &job, &sal, &comm)
		if err != nil {
			return err
		}
		fmt.Println("ENAME: ", ename, "\tJOB: ", job, "\tSAL: ", sal, "\tCOMM: ", comm)
	}
	return nil
}

func updateData(ctx context.Context, conn *sql.DB) error {
	updStmt, err := conn.PrepareContext(ctx, `UPDATE BONUS SET JOB = :1 WHERE ENAME = :2`)
	if err != nil {
		return err
	}
	defer func() {
		_ = updStmt.Close()
	}()

	_, err = updStmt.ExecContext(ctx, "designer", "foo")
	return err
}

func deleteData(ctx context.Context, conn *sql.DB) error {
	_, err := conn.ExecContext(ctx, "DELETE FROM BONUS")
	return err
}

func withTracer(f func(context.Context, *sql.DB) error, ctx context.Context, conn *sql.DB) error {
	tracer := pinpoint.FromContext(ctx)
	defer tracer.NewSpanEvent(pphttp.HandlerFuncName(f)).EndSpanEvent()
	return f(ctx, conn)
}

func query(w http.ResponseWriter, r *http.Request) {
	conn, err := sql.Open("oracle-pinpoint", "oracle://scott:tiger@localhost:1521/xe")
	if err != nil {
		fmt.Println("open error: ", err)
		return
	}
	defer func() {
		if err = conn.Close(); err != nil {
			fmt.Println("close error: ", err)
		}
	}()

	ctx := r.Context()

	if err = withTracer(insertData, ctx, conn); err != nil {
		fmt.Println("insert error: ", err)
	}
	if err = withTracer(queryData, ctx, conn); err != nil {
		fmt.Println("query error: ", err)
	}
	if err = withTracer(updateData, ctx, conn); err != nil {
		fmt.Println("update error: ", err)
	}
	if err = withTracer(queryData, ctx, conn); err != nil {
		fmt.Println("query error: ", err)
	}
	if err = withTracer(deleteData, ctx, conn); err != nil {
		fmt.Println("delete error: ", err)
	}
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoOracleTest"),
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
