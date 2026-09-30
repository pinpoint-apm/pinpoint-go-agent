package ppsaramaibm

import (
	"context"
	"testing"

	"github.com/IBM/sarama"
	"github.com/stretchr/testify/require"
)

// A nil context is a message with nothing attached; reading a value off it
// panicked out of ConsumeClaim once the agent was enabled.
func Test_newConsumerTracer_NilContext(t *testing.T) {
	startAgent(t)

	var tracer = newConsumerTracer(nil, &sarama.ConsumerMessage{Topic: "topic"})
	require.NotNil(t, tracer)
	require.NotPanics(t, func() {
		ConsumeMessageContext(func(ctx context.Context, msg *sarama.ConsumerMessage) error { return nil },
			nil, &sarama.ConsumerMessage{Topic: "topic"})
	})
	tracer.EndSpan()
}
