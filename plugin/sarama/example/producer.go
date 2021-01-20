package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/Shopify/sarama"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	psarama "github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama"
)

var fakeDB string

const topic = "sample-topic"

var producer *psarama.SyncProducer
var brokers = []string{"127.0.0.1:9092"}

func newProducer() (*psarama.SyncProducer, error) {
	config := sarama.NewConfig()
	config.Producer.Partitioner = sarama.NewRandomPartitioner
	config.Producer.RequiredAcks = sarama.WaitForAll
	config.Producer.Return.Successes = true
	config.Version = sarama.V2_3_0_0

	producer, err := psarama.NewSyncProducer(brokers, config)

	return producer, err
}

func prepareMessage(topic, message string) *sarama.ProducerMessage {
	msg := &sarama.ProducerMessage{
		Topic:     topic,
		Partition: -1,
		Value:     sarama.StringEncoder(message),
	}

	return msg
}

func save(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	msg := prepareMessage(topic, "Hello, Kafka!!")
	producer.WithContext(r.Context())
	partition, offset, err := producer.SendMessage(msg)

	if err != nil {
		fmt.Fprintf(w, "%s error occured.", err.Error())
	} else {
		fmt.Fprintf(w, "Message was saved to partion: %d.\nMessage offset is: %d.\n", partition, offset)
	}
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoKafkaProducer"),
		pinpoint.WithAgentId("GoKafkaProducerAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}

	tmp, err := newProducer()
	if err != nil {
		log.Fatalf("Could not create producer: %v ", err)
	}
	producer = tmp

	http.HandleFunc(phttp.WrapHandleFunc(agent, "save", "/save", save))
	log.Fatal(http.ListenAndServe(":8081", nil))
}
