package ppconfluentkafka

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startAgent(t *testing.T) {
	t.Helper()
	config, err := pinpoint.NewConfig(pinpoint.WithAppName("testApp"), pinpoint.WithAgentName("testAgent"))
	require.NoError(t, err)

	agent, err := pinpoint.NewTestAgent(config, t)
	require.NoError(t, err)
	t.Cleanup(agent.Shutdown)
}

// closedAddr returns a loopback address with nothing listening on it, so the
// producer's connection is refused right away instead of hanging.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

// newTestProducer produces against a broker that is not there: librdkafka
// enqueues locally and fails every message with a timeout, which is a real
// delivery report without a real broker.
func newTestProducer(t *testing.T) *Producer {
	t.Helper()
	p, err := NewProducer(&kafka.ConfigMap{
		"bootstrap.servers":  closedAddr(t) + ",second:9092",
		"message.timeout.ms": 200,
		"log_level":          0,
	})
	require.NoError(t, err)
	t.Cleanup(p.Close)
	return p
}

func newMessage(topic string) *kafka.Message {
	return &kafka.Message{
		TopicPartition: kafka.TopicPartition{Topic: &topic, Partition: kafka.PartitionAny},
		Value:          []byte("hello"),
	}
}

// A produced message carries the transaction to the consumer in its headers,
// and its delivery report still reaches the channel the application gave.
func Test_ProduceContext_DeliveryChan(t *testing.T) {
	startAgent(t)
	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/produce")
	defer tracer.EndSpan()
	// A goroutine tracer forks off the caller's open span event, the one an
	// instrumented handler always has.
	defer tracer.NewSpanEvent("handler").EndSpanEvent()

	p := newTestProducer(t)
	msg := newMessage("widgets")
	reports := make(chan kafka.Event, 1)

	require.NoError(t, p.ProduceContext(pinpoint.NewContext(context.Background(), tracer), msg, reports))

	r := headerReader{msg}
	for _, key := range []string{pinpoint.HeaderTraceId, pinpoint.HeaderSpanId, pinpoint.HeaderParentSpanId, pinpoint.HeaderParentApplicationName} {
		v, ok := r.Get(key)
		assert.True(t, ok, "the produced message is missing the %s header", key)
		assert.NotEmpty(t, v, "the %s header is empty", key)
	}
	tid, _ := r.Get(pinpoint.HeaderTraceId)
	assert.Equal(t, tracer.TransactionId().String(), tid)

	select {
	case e := <-reports:
		m, ok := e.(*kafka.Message)
		require.True(t, ok, "the delivery report must be forwarded as it is: %T", e)
		assert.Error(t, m.TopicPartition.Error, "a message to a broker that is not there fails")
	case <-time.After(5 * time.Second):
		t.Fatal("the delivery report never reached the application's channel")
	}
}

// Without a delivery channel the report goes to Events(), as raw Produce does.
func Test_ProduceContext_Events(t *testing.T) {
	startAgent(t)
	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/produce")
	defer tracer.EndSpan()

	p := newTestProducer(t)
	msg := newMessage("widgets")
	require.NoError(t, p.ProduceContext(pinpoint.NewContext(context.Background(), tracer), msg, nil))

	_, ok := headerReader{msg}.Get(pinpoint.HeaderTraceId)
	assert.True(t, ok, "the message is traced even without a delivery channel")

	// Connection failures reach Events() too; the delivery report is the
	// first *kafka.Message among them.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-p.Events():
			if _, ok := e.(*kafka.Message); ok {
				return
			}
		case <-deadline:
			t.Fatal("the delivery report never reached Events()")
		}
	}
}

// A message that already carries a Pinpoint context - an outer instrumented
// layer, or a retry re-sending the same message object - is sent untouched.
func Test_ProduceContext_NestedMessageIsNotTraced(t *testing.T) {
	startAgent(t)
	p := newTestProducer(t)
	msg := newMessage("widgets")
	msg.Headers = []kafka.Header{{Key: pinpoint.HeaderTraceId, Value: []byte("first^1^1")}}
	before := append([]kafka.Header(nil), msg.Headers...)

	reports := make(chan kafka.Event, 1)
	require.NoError(t, p.ProduceContext(context.Background(), msg, reports))
	assert.Equal(t, before, msg.Headers)
	<-reports
}

func Test_isNested(t *testing.T) {
	hdr := func(k string) []kafka.Header { return []kafka.Header{{Key: k, Value: []byte("v")}} }
	assert.False(t, isNested(&kafka.Message{}))
	assert.False(t, isNested(&kafka.Message{Headers: hdr("x-app")}))
	assert.True(t, isNested(&kafka.Message{Headers: hdr(pinpoint.HeaderTraceId)}))
	assert.True(t, isNested(&kafka.Message{Headers: hdr(pinpoint.HeaderSampled)}), "an unsampled context is a context too")
}

// A header written with an empty value is present, unlike one never written.
func Test_headerReader(t *testing.T) {
	msg := &kafka.Message{}
	w := &headerWriter{msg}
	w.Set(pinpoint.HeaderTraceId, "txid^1^1")
	w.Set(pinpoint.HeaderParentSpanId, "")

	r := headerReader{msg}
	v, ok := r.Get(pinpoint.HeaderTraceId)
	assert.True(t, ok)
	assert.Equal(t, "txid^1^1", v)
	v, ok = r.Get(pinpoint.HeaderParentSpanId)
	assert.True(t, ok, "a header with an empty value is present")
	assert.Equal(t, "", v)
	_, ok = r.Get("absent")
	assert.False(t, ok)
}

func Test_firstBroker(t *testing.T) {
	assert.Equal(t, "a:9092", firstBroker("a:9092,b:9092"))
	assert.Equal(t, "a:9092", firstBroker(" a:9092 "))
	assert.Equal(t, "Unknown", firstBroker(""))
}
