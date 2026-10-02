package ppconfluentkafka

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recv receives from ch or fails the test after five seconds.
func recv[T any](t *testing.T, ch <-chan T, name string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		require.FailNow(t, "timed out waiting for "+name)
		panic("unreachable")
	}
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
	pptest.StartAgent(t)
	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/produce")
	defer tracer.EndSpan()

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

	e := recv(t, reports, "the delivery report on the application's channel")
	m, ok := e.(*kafka.Message)
	require.True(t, ok, "the delivery report must be forwarded as it is: %T", e)
	assert.Error(t, m.TopicPartition.Error, "a message to a broker that is not there fails")
}

// Reports on a shared delivery channel arrive in the order librdkafka sends
// them, as with raw Produce: forwarding each through its own goroutine
// reordered them, and an application tracking the last delivered offset by
// them lost its place.
func Test_ProduceContext_DeliveryChanKeepsReportOrder(t *testing.T) {
	pptest.StartAgent(t)
	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/produce")
	defer tracer.EndSpan()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	p := newTestProducer(t)
	const n = 100
	reports := make(chan kafka.Event, n)
	for i := 0; i < n; i++ {
		msg := newMessage("widgets")
		msg.Opaque = i
		require.NoError(t, p.ProduceContext(ctx, msg, reports))
	}
	for i := 0; i < n; i++ {
		e := recv(t, reports, "delivery report")
		m, ok := e.(*kafka.Message)
		require.True(t, ok, "%T", e)
		require.Equal(t, i, m.Opaque, "reports out of order")
	}
}

// Without a delivery channel the report goes to Events(), as raw Produce does.
func Test_ProduceContext_Events(t *testing.T) {
	pptest.StartAgent(t)
	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/produce")
	defer tracer.EndSpan()

	p := newTestProducer(t)
	msg := newMessage("widgets")
	require.NoError(t, p.ProduceContext(pinpoint.NewContext(context.Background(), tracer), msg, nil))

	_, ok := headerReader{msg}.Get(pinpoint.HeaderTraceId)
	assert.True(t, ok, "the message is traced even without a delivery channel")

	// Connection failures reach Events() too; the delivery report is the
	// first *kafka.Message among them.
	for {
		if _, ok := recv(t, p.Events(), "the delivery report on Events()").(*kafka.Message); ok {
			return
		}
	}
}

// A message that already carries a Pinpoint context - an outer instrumented
// layer, or a retry re-sending the same message object - is sent untouched.
func Test_ProduceContext_NestedMessageIsNotTraced(t *testing.T) {
	pptest.StartAgent(t)
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
	w := &headerWriter{msg: msg}
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

// The injected headers land in a slice of the message's own: two messages
// built from one slice with spare capacity must not overwrite each other's.
func Test_headerWriter_DoesNotWriteIntoSharedCapacity(t *testing.T) {
	base := make([]kafka.Header, 0, 8)
	m1 := &kafka.Message{Headers: base}
	m2 := &kafka.Message{Headers: base}

	(&headerWriter{msg: m1}).Set(pinpoint.HeaderTraceId, "one")
	(&headerWriter{msg: m2}).Set(pinpoint.HeaderTraceId, "two")

	require.Len(t, m1.Headers, 1)
	assert.Equal(t, "one", string(m1.Headers[0].Value), "the second message's header overwrote the first's")
	assert.Empty(t, base, "the caller's slice is untouched")
}

func Test_firstBroker(t *testing.T) {
	assert.Equal(t, "a:9092", firstBroker("a:9092,b:9092"))
	assert.Equal(t, "a:9092", firstBroker(" a:9092 "))
	assert.Equal(t, "Unknown", firstBroker(""))
}
