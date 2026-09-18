package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	ppconfluentkafka "github.com/pinpoint-apm/pinpoint-go-agent/plugin/confluentkafka"
	pphttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

var producer *ppconfluentkafka.Producer

func save(w http.ResponseWriter, r *http.Request) {
	topic := "go-kafka-test"
	msg := &kafka.Message{
		TopicPartition: kafka.TopicPartition{Topic: &topic, Partition: kafka.PartitionAny},
		Value:          []byte("Hello, Kafka!!"),
	}

	deliveryChan := make(chan kafka.Event, 1)
	if err := producer.ProduceContext(r.Context(), msg, deliveryChan); err != nil {
		fmt.Fprintf(w, "%s error occured.", err.Error())
		return
	}

	m := (<-deliveryChan).(*kafka.Message)
	if m.TopicPartition.Error != nil {
		fmt.Fprintf(w, "%s error occured.", m.TopicPartition.Error.Error())
	} else {
		fmt.Fprintf(w, "Message was saved to %s\n", m.TopicPartition)
	}
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoKafkaProducer"),
		pinpoint.WithAgentName("GoKafkaProducerAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	producer, err = ppconfluentkafka.NewProducer(&kafka.ConfigMap{"bootstrap.servers": "localhost:9092"})
	if err != nil {
		log.Fatalf("Could not create producer: %v ", err)
	}
	defer producer.Close()

	http.HandleFunc("/save", pphttp.WrapHandlerFunc(save))
	log.Fatal(http.ListenAndServe(":9023", nil))
}
