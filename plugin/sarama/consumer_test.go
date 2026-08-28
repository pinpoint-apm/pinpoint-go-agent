package ppsarama

import (
	"context"
	"testing"

	"github.com/Shopify/sarama"
)

// An empty or mistyped broker-address value must fall back to Unknown instead
// of panicking out of ConsumeClaim and killing the consumer.
func Test_newConsumerTracer_EmptyBrokerAddress(t *testing.T) {
	msg := &sarama.ConsumerMessage{Topic: "topic"}

	for _, ctx := range []context.Context{
		NewContext(context.Background(), []string{}),
		context.WithValue(context.Background(), contextKey, "not-a-slice"),
	} {
		tracer := newConsumerTracer(ctx, msg)
		if tracer == nil {
			t.Fatal("no tracer returned")
		}
		tracer.EndSpan()
	}
}
