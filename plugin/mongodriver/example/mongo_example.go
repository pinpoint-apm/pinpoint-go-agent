package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	pphttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	ppmongo "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongodriver"
)

func mongodb(w http.ResponseWriter, r *http.Request) {
	opts := options.Client()
	opts.ApplyURI("mongodb://localhost:27017")
	opts.Monitor = ppmongo.NewMonitor()
	ctx := context.Background()

	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		panic(err)
	}
	defer client.Disconnect(ctx)

	collection := client.Database("testdb").Collection("example")
	_, err = collection.InsertOne(r.Context(), bson.M{"foo": "bar", "apm": "pinpoint"})
	if err != nil {
		panic(err)
	}
	io.WriteString(w, "insert success")
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoMongoTest"),
		pinpoint.WithAgentId("GoMongoTestAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	http.HandleFunc("/mongo", pphttp.WrapHandlerFunc(mongodb))

	http.ListenAndServe(":9020", nil)
}
