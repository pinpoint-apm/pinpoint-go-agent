package main

import (
	"io"
	"log"
	"net/http"
	"os"

	"github.com/apache/cassandra-gocql-driver/v2"
	ppgocqlv2 "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gocqlv2/v2"
	pphttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

func doCassandra(w http.ResponseWriter, r *http.Request) {
	cluster := gocql.NewCluster("127.0.0.1")
	cluster.Keyspace = "example"
	cluster.Consistency = gocql.Quorum

	observer := ppgocqlv2.NewObserver()
	cluster.QueryObserver = observer
	cluster.BatchObserver = observer

	session, err := cluster.CreateSession()
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, err.Error())
		return
	}
	defer session.Close()

	ctx := r.Context()

	//query := session.Query(`INSERT INTO tweet (timeline, id, text) VALUES (?, ?, ?)`, "me", gocql.TimeUUID(), "hello world")
	//if err := query.WithContext(ctx).Exec(); err != nil {
	//	log.Fatal(err)
	//}

	var id gocql.UUID
	var text string

	query := session.Query(`SELECT id, text FROM tweet WHERE timeline = ? LIMIT 1`, "me")
	if err := query.Consistency(gocql.One).ScanContext(ctx, &id, &text); err != nil {
		log.Println(err)
	}
	io.WriteString(w, "Tweet:"+text)

	query = session.Query(`SELECT id, text FROM tweet WHERE timeline = ?`, "me")
	iter := query.IterContext(ctx)
	for iter.Scan(&id, &text) {
		io.WriteString(w, "Tweet:"+text)
	}
	if err := iter.Close(); err != nil {
		log.Println(err)
	}
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoCassandraV2Test"),
		pinpoint.WithAgentName("GoCassandraV2TestAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()
	defer pinpoint.ShutdownOnSignal(agent)() // SIGTERM, SIGINT

	http.HandleFunc("/cassandra", pphttp.WrapHandlerFunc(doCassandra))

	http.ListenAndServe(":9025", nil)
}
