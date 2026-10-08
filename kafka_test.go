package pinpoint

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The RPC name is the consumer span's title on the Pinpoint screen, and it is
// what distinguishes one partition's consumption from another's.
func TestKafkaConsumerMessage_RpcName(t *testing.T) {
	assert.Equal(t, "kafka://topic=widgets?partition=3&offset=42",
		KafkaConsumerMessage{Topic: "widgets", Partition: 3, Offset: 42}.RpcName())
	// The first message of a fresh partition is offset 0, not an absent one.
	assert.Equal(t, "kafka://topic=widgets?partition=0&offset=0", KafkaConsumerMessage{Topic: "widgets"}.RpcName())
}

// The broker is the consumer span's endpoint on the server map: the one the
// application gave, failing that the host the producer stamped on the record,
// and Unknown with neither. A disabled agent traces nothing.
func TestNewKafkaConsumerTracer(t *testing.T) {
	c, err := NewConfig(WithAppName("kafka"))
	require.NoError(t, err)
	a, err := NewTestAgent(c)
	require.NoError(t, err)
	defer a.Shutdown()

	hostHeader := http.Header{HeaderHost: {"broker9:9092"}}
	for _, tt := range []struct {
		name   string
		broker string
		hdr    http.Header
		want   string
	}{
		{"given broker wins", "broker1:9092", hostHeader, "broker1:9092"},
		{"host header from the producer", "", hostHeader, "broker9:9092"},
		{"neither", "", http.Header{}, "Unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msg := KafkaConsumerMessage{Topic: "widgets", Partition: 1, Offset: 7, Broker: tt.broker}
			tracer := NewKafkaConsumerTracer("Kafka Consumer Invocation", msg, HttpHeaderReader(tt.hdr))
			var span struct{ RpcName, EndPoint, RemoteAddr string }
			require.NoError(t, json.Unmarshal(SpanJSON(tracer), &span))
			tracer.EndSpan()

			assert.Equal(t, msg.RpcName(), span.RpcName)
			assert.Equal(t, tt.want, span.EndPoint)
			assert.Equal(t, tt.want, span.RemoteAddr)
		})
	}

	a.Shutdown()
	assert.Equal(t, NoopTracer(), NewKafkaConsumerTracer("Kafka Consumer Invocation", KafkaConsumerMessage{}, HttpHeaderReader(http.Header{})),
		"a disabled agent must hand back the noop tracer")
}
