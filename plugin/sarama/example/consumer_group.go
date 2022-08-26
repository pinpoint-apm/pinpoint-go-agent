package main

import (
	"context"
	"fmt"
	"github.com/Shopify/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	psarama "github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama"
	"log"
	"os"
)

type exampleConsumerGroupHandler struct {
	agent pinpoint.Agent
}

func (exampleConsumerGroupHandler) Setup(_ sarama.ConsumerGroupSession) error   { return nil }
func (exampleConsumerGroupHandler) Cleanup(_ sarama.ConsumerGroupSession) error { return nil }
func (h exampleConsumerGroupHandler) ConsumeClaim(sess sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	ctx := sess.Context()
	for msg := range claim.Messages() {
		_ = psarama.ConsumeMessageContext(process, ctx, msg, h.agent)
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

	config := sarama.NewConfig()
	config.Version = sarama.V2_3_0_0 // specify appropriate version
	config.Consumer.Return.Errors = true

	group, err := sarama.NewConsumerGroup([]string{"localhost:9092"}, "my-group", config)
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
	ctx := context.Background()
	topics := []string{"go-sarama-test"}
	handler := exampleConsumerGroupHandler{agent: agent}

	for {
		err := group.Consume(ctx, topics, handler)
		if err != nil {
			panic(err)
		}
	}
}
