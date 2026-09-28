package ppconfluentkafka

import (
	"testing"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WrapProducer wraps a producer created elsewhere exactly as NewProducer
// wraps the one it creates: the broker is the configuration's first
// bootstrap server, "Unknown" without one.
func TestWrapProducer(t *testing.T) {
	addr := closedAddr(t)
	conf := &kafka.ConfigMap{"bootstrap.servers": addr + ",second:9092", "message.timeout.ms": 200, "log_level": 0}
	raw, err := kafka.NewProducer(conf)
	require.NoError(t, err)
	t.Cleanup(raw.Close)

	p := WrapProducer(raw, conf)
	assert.Same(t, raw, p.Producer, "the wrapper must keep the producer it was given")
	assert.Equal(t, addr, p.broker)
	assert.Equal(t, "Unknown", WrapProducer(raw, nil).broker)
	assert.Equal(t, "Unknown", WrapProducer(raw, &kafka.ConfigMap{}).broker)
}
