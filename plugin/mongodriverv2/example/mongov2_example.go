package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	pphttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	ppmongov2 "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongodriverv2"
)

func mongodb(w http.ResponseWriter, r *http.Request) {
	opts := options.Client()
	opts.ApplyURI("mongodb://localhost:27017")
	opts.Monitor = ppmongov2.NewMonitor()

	client, err := mongo.Connect(opts)
	if err != nil {
		panic(err)
	}
	defer client.Disconnect(context.Background())

	collection := client.Database("testdb").Collection("example")
	_, err = collection.InsertOne(r.Context(), bson.M{"foo": "bar", "apm": "pinpoint"})
	if err != nil {
		panic(err)
	}
	io.WriteString(w, "insert success")
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoMongoV2Test"),
		pinpoint.WithAgentId("GoMongoV2TestAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	http.HandleFunc("/mongo", pphttp.WrapHandlerFunc(mongodb))

	http.ListenAndServe(":9025", nil)
}
