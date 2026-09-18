package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	ppconfluentkafka "github.com/pinpoint-apm/pinpoint-go-agent/plugin/confluentkafka"
)

func processMessage(ctx context.Context, msg *kafka.Message) error {
	tracer := pinpoint.FromContext(ctx)
	defer tracer.NewSpanEvent("processMessage").EndSpanEvent()

	fmt.Printf("Message on %s: %s\n", msg.TopicPartition, string(msg.Value))
	return nil
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoKafkaConsumer"),
		pinpoint.WithAgentName("GoKafkaConsumerAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	broker := "localhost:9092"
	consumer, err := kafka.NewConsumer(&kafka.ConfigMap{
		"bootstrap.servers": broker,
		"group.id":          "go-kafka-test-group",
		"auto.offset.reset": "earliest",
	})
	if err != nil {
		log.Fatalf("Could not create consumer: %v", err)
	}
	defer consumer.Close()

	if err := consumer.Subscribe("go-kafka-test", nil); err != nil {
		log.Fatalf("Could not subscribe: %v", err)
	}

	ctx := ppconfluentkafka.NewContext(context.Background(), broker)
	for {
		msg, err := consumer.ReadMessage(-1)
		if err != nil {
			log.Printf("consumer error: %v", err)
			continue
		}
		_ = ppconfluentkafka.ConsumeMessageContext(processMessage, ctx, msg)
	}
}
