// Backend app of the distributed tracing demo (see README.md).
//
// Shows the server-side tracing surface of the Go agent in one request flow:
//
//	GET /api/members
//	  → WrapHandlerFunc opens the root span with the trace context extracted
//	    from the request headers, making it a child of the proxy's client
//	    span event in the same trace
//	  → the MySQL query is traced by the mysql plugin as a child span event
//	     · the "mysql-pinpoint" driver records the statement, its bind
//	       variables and the database host, and makes the database its own
//	       node in the server map
//	     · the plugin finds the tracer in the context handed to
//	       QueryContext - here the request context, which the handler
//	       wrapper filled in
//	  → follow-up work is handed to a goroutine traced with an async span
//	     · NewGoroutineTracer hangs the async span off the span event that is
//	       open on the calling goroutine, so that event must still be open
//	     · the tracer it returns belongs to that goroutine alone
//	  → the wrapper records the response and ends the span
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	pphttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	_ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql"
)

const membersQuery = "SELECT id, name FROM members WHERE id > ?"

var db *sql.DB

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

type member struct {
	Id   int    `json:"id"`
	Name string `json:"name"`
}

// queryMembers runs the members query on the traced driver. Handing it the
// request context is all the tracing this needs: the driver picks the tracer
// out of it and records the query as a span event of the request's span.
func queryMembers(ctx context.Context) ([]member, error) {
	rows, err := db.QueryContext(ctx, membersQuery, 0)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	members := []member{}
	for rows.Next() {
		var m member
		if err := rows.Scan(&m.Id, &m.Name); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// runAsyncAudit hands follow-up work to a goroutine traced with an async span.
func runAsyncAudit(tracer pinpoint.Tracer) {
	// The async span hangs off this event, so it must still be open when
	// NewGoroutineTracer is called.
	defer tracer.NewSpanEvent("audit.schedule").EndSpanEvent()

	wg := &sync.WaitGroup{}
	wg.Add(1)

	// The async tracer is created on this goroutine and then used only by the
	// worker: one tracer must never be used by two goroutines at once.
	go func(async pinpoint.Tracer) {
		defer wg.Done()
		defer async.EndSpan() //!!must be called
		defer async.NewSpanEvent("audit.write").EndSpanEvent()

		time.Sleep(30 * time.Millisecond)
	}(tracer.NewGoroutineTracer())

	// Waited for so the traced goroutine never races agent shutdown.
	wg.Wait()
}

func members(w http.ResponseWriter, r *http.Request) {
	rows, err := queryMembers(r.Context())
	if err != nil {
		// The failed query is already recorded on its own span event; the
		// handler error is recorded on the span by the wrapper.
		log.Println("query failed:", err)
		http.Error(w, `{"error":"query failed"}`, http.StatusServiceUnavailable)
		return
	}

	runAsyncAudit(pinpoint.FromContext(r.Context()))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string][]member{"members": rows})
}

// setup creates and seeds the demo table. Its failure is not fatal: the app
// still boots and answers 503, so the demo runs without a database at hand.
func setup() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, stmt := range []string{
		"CREATE TABLE IF NOT EXISTS members (id INT PRIMARY KEY, name VARCHAR(64) NOT NULL)",
		"REPLACE INTO members (id, name) VALUES (1, 'pinpoint'), (2, 'naver'), (3, 'go-agent')",
	} {
		// Untraced: no tracer in the context, so the driver records nothing.
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			log.Println("table setup failed, /api/members will answer 503:", err)
			return
		}
	}
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoDbServerExample"),
		pinpoint.WithAgentId("GoDbServerExampleAgent"),
		pinpoint.WithCollectorHost(envOr("PINPOINT_GO_COLLECTOR_HOST", "localhost")),
		// Record only the User-Agent request header on the span (see doc/config.md).
		pphttp.WithHttpServerRecordRequestHeader([]string{"User-Agent"}),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Printf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	// "mysql-pinpoint" is the traced driver the mysql plugin registers; it is
	// the only change a database call needs to show up in Pinpoint.
	db, err = sql.Open("mysql-pinpoint", envOr("MYSQL_DSN", "root:p123@tcp(127.0.0.1:3306)/testdb"))
	if err != nil {
		log.Fatalf("cannot open database: %v", err)
	}
	defer db.Close()
	setup()

	addr := envOr("ADDR", ":8081")
	http.HandleFunc("/api/members", pphttp.WrapHandlerFunc(members, "Go DB Server"))

	log.Println("db server listening on", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
