package ppconfluentkafka

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func consumed(topic string, partition int32, offset int64, headers ...kafka.Header) *kafka.Message {
	return &kafka.Message{
		TopicPartition: kafka.TopicPartition{Topic: &topic, Partition: partition, Offset: kafka.Offset(offset)},
		Headers:        headers,
	}
}

func Test_makeRpcName(t *testing.T) {
	assert.Equal(t, "kafka://topic=widgets?partition=3&offset=42", makeRpcName(consumed("widgets", 3, 42)))
	assert.Equal(t, "kafka://topic=?partition=0&offset=0", makeRpcName(&kafka.Message{}), "a nil topic must not panic")
}

type spanFields struct {
	RpcName    string
	EndPoint   string
	RemoteAddr string
}

func readSpan(t *testing.T, tracer pinpoint.Tracer) spanFields {
	t.Helper()
	var f spanFields
	require.NoError(t, json.Unmarshal(tracer.JsonString(), &f))
	return f
}

// The broker comes from the context the application built, or from the host
// the producer stamped on the message, or is Unknown.
func Test_newConsumerTracer_Broker(t *testing.T) {
	startAgent(t)
	for _, tt := range []struct {
		name string
		ctx  context.Context
		msg  *kafka.Message
		want string
	}{
		{"from the context", NewContext(context.Background(), "broker1:9092,broker2:9092"), consumed("w", 0, 0), "broker1:9092"},
		{"from the producer's header", context.Background(), consumed("w", 0, 0, kafka.Header{Key: pinpoint.HeaderHost, Value: []byte("broker3:9092")}), "broker3:9092"},
		{"neither", context.Background(), consumed("w", 0, 0), "Unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tracer := newConsumerTracer(tt.ctx, tt.msg)
			f := readSpan(t, tracer)
			tracer.EndSpan()
			assert.Equal(t, tt.want, f.EndPoint)
			assert.Equal(t, tt.want, f.RemoteAddr)
			assert.Equal(t, "kafka://topic=w?partition=0&offset=0", f.RpcName)
		})
	}
}

// The consumer continues the producer's transaction from the message headers.
func Test_ConsumeMessageContext(t *testing.T) {
	startAgent(t)
	producerTracer := pinpoint.GetAgent().NewSpanTracer("test", "/produce")
	defer producerTracer.EndSpan()

	msg := consumed("widgets", 1, 7)
	producerTracer.Inject(&headerWriter{msg})

	want := errors.New("handler failed")
	var got pinpoint.Tracer
	err := ConsumeMessageContext(func(ctx context.Context, m *kafka.Message) error {
		got = pinpoint.FromContext(ctx)
		return want
	}, NewContext(context.Background(), "broker:9092"), msg)

	assert.ErrorIs(t, err, want, "the handler's error must come back unchanged")
	require.NotNil(t, got)
	assert.Equal(t, producerTracer.TransactionId(), got.TransactionId(), "the consumer span must continue the producer's transaction")

	assert.ErrorIs(t, ConsumeMessageContext(nil, context.Background(), nil), errNilConsumerMessage)
}
