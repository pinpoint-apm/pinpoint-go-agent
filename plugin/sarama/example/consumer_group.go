package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/Shopify/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama"
)

type exampleConsumerGroupHandler struct {
}

func (exampleConsumerGroupHandler) Setup(_ sarama.ConsumerGroupSession) error   { return nil }
func (exampleConsumerGroupHandler) Cleanup(_ sarama.ConsumerGroupSession) error { return nil }
func (h exampleConsumerGroupHandler) ConsumeClaim(sess sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	ctx := sess.Context()
	for msg := range claim.Messages() {
		_ = ppsarama.ConsumeMessageContext(process, ctx, msg)
		sess.MarkMessage(msg, "")
	}
	return nil
}

func process(ctx context.Context, msg *sarama.ConsumerMessage) error {
	tracer := pinpoint.FromContext(ctx)
	defer tracer.NewSpanEvent("process").EndSpanEvent()

	fmt.Printf("Message topic:%q partition:%d offset:%d\n", msg.Topic, msg.Partition, msg.Offset)

	return nil
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoKafkaConsumerGroup"),
		pinpoint.WithAgentId("GoKafkaConsumerGroupAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	config := sarama.NewConfig()
	config.Version = sarama.V2_3_0_0 // specify appropriate version
	config.Consumer.Return.Errors = true

	broker := []string{"localhost:9092"}
	group, err := sarama.NewConsumerGroup(broker, "my-group", config)
	if err != nil {
		panic(err)
	}
	defer func() { _ = group.Close() }()

	// Track errors
	go func() {
		for err := range group.Errors() {
			fmt.Println("ERROR", err)
		}
	}()

	// Iterate over consumer sessions.
	ctx := ppsarama.NewContext(context.Background(), broker)
	topics := []string{"go-sarama-test"}
	handler := exampleConsumerGroupHandler{}

	for {
		err := group.Consume(ctx, topics, handler)
		if err != nil {
			panic(err)
		}
	}
}
