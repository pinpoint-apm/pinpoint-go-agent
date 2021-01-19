package main

import (
	"io"
	"log"
	"net/http"

	"github.com/gocql/gocql"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	pgocql "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gocql"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func doCassandra(w http.ResponseWriter, r *http.Request) {
	cluster := gocql.NewCluster("127.0.0.1")
	cluster.Keyspace = "example"
	cluster.Consistency = gocql.Quorum

	observer := pgocql.NewObserver()
	cluster.QueryObserver = observer
	cluster.BatchObserver = observer

	session, _ := cluster.CreateSession()
	defer session.Close()

	ctx := r.Context()

	//query := session.Query(`INSERT INTO tweet (timeline, id, text) VALUES (?, ?, ?)`, "me", gocql.TimeUUID(), "hello world")
	//if err := query.WithContext(ctx).Exec(); err != nil {
	//	log.Fatal(err)
	//}

	var id gocql.UUID
	var text string

	query := session.Query(`SELECT id, text FROM tweet WHERE timeline = ? LIMIT 1`, "me")
	if err := query.WithContext(ctx).Consistency(gocql.One).Scan(&id, &text); err != nil {
		log.Println(err)
	}
	io.WriteString(w, "Tweet:"+text)

	query = session.Query(`SELECT id, text FROM tweet WHERE timeline = ?`, "me")
	iter := query.WithContext(ctx).Iter()
	for iter.Scan(&id, &text) {
		io.WriteString(w, "Tweet:"+text)
	}
	if err := iter.Close(); err != nil {
		log.Println(err)
	}
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoCassandraTest"),
		pinpoint.WithAgentId("GoCassandraTestAgent"),
		pinpoint.WithCollectorHost("localhost"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}

	http.HandleFunc(phttp.WrapHandleFunc(agent, "doCassandra", "/cassandra", doCassandra))

	http.ListenAndServe(":9000", nil)
	agent.Shutdown()
}
