package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/Shopify/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama"
)

var fakeDB string

var producer sarama.SyncProducer

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

	topic := "go-sarama-test"
	msg := prepareMessage(topic, "Hello, Kafka!!")
	ppsarama.WithContext(r.Context(), producer)
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
	defer agent.Shutdown()

	config := sarama.NewConfig()
	config.Producer.Partitioner = sarama.NewRandomPartitioner
	config.Producer.RequiredAcks = sarama.WaitForAll
	config.Producer.Return.Successes = true
	config.Version = sarama.V2_3_0_0

	var brokers = []string{"localhost:9092"}
	producer, err = ppsarama.NewSyncProducer(brokers, config)
	if err != nil {
		log.Fatalf("Could not create producer: %v ", err)
	}

	http.HandleFunc("/save", pphttp.WrapHandlerFunc(save))
	log.Fatal(http.ListenAndServe(":8081", nil))
}
