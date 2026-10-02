package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/e2e/internal/e2e"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/testapp"
)

// The SQL endpoints exercise the agent's database/sql driver wrapper, which is
// where SQL normalization, metadata publication and bind-value recording live.
// They need no database: the wrapped testapp.FakeDriver accepts every statement
// and returns nothing, so the traced code path is real while the storage is not.

var (
	db *sql.DB
	// dbFaults makes every traced statement sleep and turns roughly 30% of
	// them into error spans. Off by default: the sleeps would dominate a load
	// pass, which would then measure the injected sleep rather than the agent.
	dbFaults = os.Getenv("PINPOINT_E2E_DB_FAULTS") != "" && os.Getenv("PINPOINT_E2E_DB_FAULTS") != "0"

	dbErrorMessages = []string{
		"Connection timed out after 30s",
		"Deadlock found when trying to get lock; try restarting transaction",
		"Lost connection to MySQL server during query",
		"Query execution was interrupted",
		"Too many connections",
		"Lock wait timeout exceeded; try restarting transaction",
		"Duplicate entry for key 'PRIMARY'",
	}
)

func initDB() {
	sql.Register("pinpoint-e2e-fake", pinpoint.WrapSQLDriver(testapp.FakeDriver{}, pinpoint.DBInfo{
		DBType:    pinpoint.ServiceTypeMysql,
		QueryType: pinpoint.ServiceTypeMysqlExecuteQuery,
		DBName:    "e2e_test",
		DBHost:    "localhost:33060",
	}))
	var err error
	if db, err = sql.Open("pinpoint-e2e-fake", "e2e-dsn"); err != nil {
		log.Fatalf("open fake database: %v", err)
	}
	if dbFaults {
		log.Printf("DB fault injection ON: per-statement sleep and ~30%% error spans")
	}
}

// traceSQL runs one statement through the instrumented driver.
func traceSQL(ctx context.Context, tracer pinpoint.Tracer, query string, args ...any) {
	if dbFaults {
		time.Sleep(time.Duration(rand.Intn(100)+1) * time.Millisecond)
	}
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		tracer.SpanEvent().SetError(err, "SQL error")
	}
	if dbFaults && rand.Intn(10) < 3 {
		// The statement itself succeeded; record the failure the way a driver
		// error would surface so error spans are exercised too.
		tracer.NewSpanEvent("SQL_fault")
		tracer.SpanEvent().SetServiceType(pinpoint.ServiceTypeMysqlExecuteQuery)
		tracer.SpanEvent().SetError(
			errors.New(dbErrorMessages[rand.Intn(len(dbErrorMessages))]), "MySQL_Error")
		tracer.EndSpanEvent()
	}
}

// onDbBatch traces a create/insert/select/delete cycle.
func onDbBatch(w http.ResponseWriter, r *http.Request) {
	defer track()()
	tracer := newSpan(r)
	ctx := pinpoint.NewContext(r.Context(), tracer)
	batchSize := e2e.IntParam(r, "size", 20, 1, 200)

	traceSQL(ctx, tracer, "CREATE TABLE IF NOT EXISTS e2e_batch "+
		"(id INT AUTO_INCREMENT PRIMARY KEY, val VARCHAR(100), num INT)")
	for i := 0; i < batchSize; i++ {
		traceSQL(ctx, tracer, "INSERT INTO e2e_batch (val, num) VALUES (?, ?)",
			"item_"+strconv.Itoa(i), int64(i))
	}
	traceSQL(ctx, tracer, "SELECT * FROM e2e_batch ORDER BY id DESC LIMIT ?", int64(batchSize))
	traceSQL(ctx, tracer, "DELETE FROM e2e_batch")

	setTraceHeaders(w, tracer)
	e2e.WriteJSON(w, http.StatusOK, map[string]any{"batch_size": batchSize, "status": "ok"})
	finishSpan(w, r, tracer, http.StatusOK)
}

// onDbComplex traces a CRUD cycle with joins, subqueries and aggregation, plus a
// transaction so the commit/rollback instrumentation is exercised.
func onDbComplex(w http.ResponseWriter, r *http.Request) {
	defer track()()
	tracer := newSpan(r)
	ctx := pinpoint.NewContext(r.Context(), tracer)

	traceSQL(ctx, tracer, "CREATE TABLE IF NOT EXISTS e2e_orders "+
		"(id INT AUTO_INCREMENT PRIMARY KEY, user_id INT, amount DECIMAL(10,2), "+
		"status VARCHAR(20), created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)")
	traceSQL(ctx, tracer, "CREATE TABLE IF NOT EXISTS e2e_users "+
		"(id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(100), email VARCHAR(100), age INT)")
	traceSQL(ctx, tracer, "DELETE FROM e2e_orders")
	traceSQL(ctx, tracer, "DELETE FROM e2e_users")

	const insertUser = "INSERT INTO e2e_users (name, email, age) VALUES (?, ?, ?)"
	for _, user := range []struct {
		name  string
		email string
		age   int64
	}{{"Alice", "alice@t.com", 28}, {"Bob", "bob@t.com", 35}, {"Charlie", "charlie@t.com", 42}} {
		traceSQL(ctx, tracer, insertUser, user.name, user.email, user.age)
	}

	const insertOrder = "INSERT INTO e2e_orders (user_id, amount, status) VALUES (?, ?, ?)"
	traceSQL(ctx, tracer, insertOrder, int64(1), 99.50, "completed")
	traceSQL(ctx, tracer, insertOrder, int64(1), 150.00, "pending")
	traceSQL(ctx, tracer, insertOrder, int64(2), 200.00, "completed")
	traceSQL(ctx, tracer, insertOrder, int64(3), 75.25, "completed")

	traceSQL(ctx, tracer, "SELECT u.name, o.amount, o.status FROM e2e_users u "+
		"JOIN e2e_orders o ON u.id = o.user_id ORDER BY o.amount DESC")
	traceSQL(ctx, tracer, "SELECT u.name, COUNT(o.id) as order_count, SUM(o.amount) as total "+
		"FROM e2e_users u LEFT JOIN e2e_orders o ON u.id = o.user_id GROUP BY u.id, u.name ORDER BY total DESC")
	traceSQL(ctx, tracer, "SELECT name, email FROM e2e_users WHERE id IN "+
		"(SELECT DISTINCT user_id FROM e2e_orders WHERE status = ?)", "completed")
	traceSQL(ctx, tracer, "SELECT name, age, CASE WHEN age < 30 THEN 'Young' "+
		"WHEN age < 40 THEN 'Middle' ELSE 'Senior' END as age_group FROM e2e_users ORDER BY age")

	// A transaction adds ConnBeginTx/Commit span events to the mix.
	if tx, err := db.BeginTx(ctx, nil); err == nil {
		tx.ExecContext(ctx, "UPDATE e2e_users SET age = age + 1 WHERE name = ?", "Alice")
		if err := tx.Commit(); err != nil {
			tracer.SpanEvent().SetError(err, "SQL error")
		}
	}
	traceSQL(ctx, tracer, "SELECT * FROM non_existent_table_xyz")

	setTraceHeaders(w, tracer)
	e2e.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "queries": "complex"})
	finishSpan(w, r, tracer, http.StatusOK)
}
