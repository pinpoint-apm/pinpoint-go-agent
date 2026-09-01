package ppsaramaibm

import (
	"context"
	"errors"
	"testing"

	"github.com/IBM/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinpointHeaders are the distributed tracing headers Inject writes; a consumer
// continues the transaction from them.
var pinpointHeaders = []string{
	pinpoint.HeaderTraceId,
	pinpoint.HeaderSpanId,
	pinpoint.HeaderParentSpanId,
	pinpoint.HeaderParentApplicationName,
}

type stubSyncProducer struct {
	sarama.SyncProducer
	sent    []*sarama.ProducerMessage
	batches [][]*sarama.ProducerMessage
	err     error
}

func (s *stubSyncProducer) SendMessage(msg *sarama.ProducerMessage) (int32, int64, error) {
	s.sent = append(s.sent, msg)
	return 3, 42, s.err
}

func (s *stubSyncProducer) SendMessages(msgs []*sarama.ProducerMessage) error {
	s.batches = append(s.batches, msgs)
	return s.err
}

func (s *stubSyncProducer) Close() error { return nil }

// capturingTracer records what the producer wrapper puts on a span event. A
// real tracer's recorders are write-only, so this stands in for one. Span
// events nest like the real tracer's - a batch send opens all of them before
// closing any - so open events are kept on a stack.
type capturingTracer struct {
	pinpoint.Tracer
	events []*capturedEvent
	open   []*capturedEvent
}

func newCapturingTracer() *capturingTracer {
	return &capturingTracer{Tracer: pinpoint.NoopTracer()}
}

func (t *capturingTracer) IsSampled() bool { return true }

func (t *capturingTracer) NewSpanEvent(operation string) pinpoint.Tracer {
	e := &capturedEvent{
		SpanEventRecorder: t.Tracer.SpanEvent(),
		operation:         operation,
		annotations:       map[int32]string{},
	}
	t.events = append(t.events, e)
	t.open = append(t.open, e)
	return t
}

func (t *capturingTracer) SpanEvent() pinpoint.SpanEventRecorder { return t.open[len(t.open)-1] }

func (t *capturingTracer) EndSpanEvent() {
	e := t.open[len(t.open)-1]
	t.open = t.open[:len(t.open)-1]
	e.ended = true
}

type capturedEvent struct {
	pinpoint.SpanEventRecorder
	operation   string
	serviceType int32
	destination string
	annotations map[int32]string
	ended       bool
}

func (e *capturedEvent) SetServiceType(typ int32) { e.serviceType = typ }
func (e *capturedEvent) SetDestination(id string) { e.destination = id }

func (e *capturedEvent) Annotations() pinpoint.Annotation {
	return capturedAnnotation{Annotation: e.SpanEventRecorder.Annotations(), into: e.annotations}
}

type capturedAnnotation struct {
	pinpoint.Annotation
	into map[int32]string
}

func (a capturedAnnotation) AppendString(key int32, s string) { a.into[key] = s }

// A produced message is one span event, and the tracing headers it carries are
// the only channel the consumer can continue the transaction through.
func Test_syncProducer_SendMessageContext(t *testing.T) {
	startAgent(t)

	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/produce")
	defer tracer.EndSpan()

	stub := &stubSyncProducer{}
	p := &syncProducer{SyncProducer: stub, addrs: []string{"broker1:9092"}, ctx: context.Background()}
	msg := &sarama.ProducerMessage{Topic: "widgets"}

	partition, offset, err := p.SendMessageContext(pinpoint.NewContext(context.Background(), tracer), msg)

	require.NoError(t, err)
	assert.Equal(t, int32(3), partition, "the underlying producer's partition must come back unchanged")
	assert.Equal(t, int64(42), offset, "the underlying producer's offset must come back unchanged")
	require.Len(t, stub.sent, 1)
	assert.Same(t, msg, stub.sent[0], "the underlying producer received a different message")

	writer := &distributedTracingContextWriterProducer{msg: msg}
	for _, key := range pinpointHeaders {
		assert.NotEmpty(t, writer.Get(key), "the produced message is missing the %s header", key)
	}
	assert.Equal(t, tracer.TransactionId().String(), writer.Get(pinpoint.HeaderTraceId))
}

// The span event names the topic and the broker, which is what puts the
// message on the right node of the server map.
func Test_newSyncProducerTracer_RecordsTopicAndBroker(t *testing.T) {
	tracer := newCapturingTracer()
	msg := &sarama.ProducerMessage{Topic: "widgets"}

	newSyncProducerTracer(pinpoint.NewContext(context.Background(), tracer),
		[]string{"broker1:9092", "broker2:9092"}, msg).EndSpanEvent()

	require.Len(t, tracer.events, 1, "one message must produce exactly one span event")
	e := tracer.events[0]
	assert.Equal(t, "sarama.SyncProducer.SendMessage", e.operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeKafkaClient), e.serviceType)
	assert.Equal(t, "broker1:9092", e.destination, "the first broker names the destination")
	assert.Equal(t, "widgets", e.annotations[pinpoint.AnnotationKafkaTopic])
	assert.True(t, e.ended, "the span event was left open")
}

// A batch send is one span event per message - each one is a separate record
// on a topic - and all of them have to be closed once the batch returns.
func Test_syncProducer_SendMessagesContext(t *testing.T) {
	startAgent(t)
	tracer := newCapturingTracer()
	stub := &stubSyncProducer{err: errors.New("broker unavailable")}
	p := &syncProducer{SyncProducer: stub, addrs: []string{"broker1:9092"}, ctx: context.Background()}

	msgs := []*sarama.ProducerMessage{{Topic: "widgets"}, {Topic: "gadgets"}}
	err := p.SendMessagesContext(pinpoint.NewContext(context.Background(), tracer), msgs)

	assert.ErrorIs(t, err, stub.err, "the batch error must come back unchanged")
	require.Len(t, stub.batches, 1)
	assert.Len(t, stub.batches[0], 2, "the whole batch must reach the underlying producer")

	require.Len(t, tracer.events, 2, "each message in a batch is its own record, so its own span event")
	for i, want := range []string{"widgets", "gadgets"} {
		assert.Equal(t, want, tracer.events[i].annotations[pinpoint.AnnotationKafkaTopic],
			"topic annotation %d", i)
		assert.True(t, tracer.events[i].ended, "span event %d was left open", i)
	}
}

// The deprecated WithContext form has to reach the same instrumentation as
// SendMessageContext, through the context stored on the producer.
func Test_syncProducer_WithContext(t *testing.T) {
	startAgent(t)
	tracer := newCapturingTracer()
	stub := &stubSyncProducer{}
	p := &syncProducer{SyncProducer: stub, addrs: []string{"broker1:9092"}, ctx: context.Background()}

	WithContext(pinpoint.NewContext(context.Background(), tracer), p)

	_, _, err := p.SendMessage(&sarama.ProducerMessage{Topic: "widgets"})
	require.NoError(t, err)
	require.NoError(t, p.SendMessages([]*sarama.ProducerMessage{{Topic: "gadgets"}}))

	require.Len(t, tracer.events, 2)
	for i, want := range []string{"widgets", "gadgets"} {
		assert.Equal(t, want, tracer.events[i].annotations[pinpoint.AnnotationKafkaTopic],
			"topic annotation %d", i)
		assert.True(t, tracer.events[i].ended, "span event %d was left open", i)
	}
}

// A producer used without any pinpoint context still has to produce; the
// wrapper records nothing on a noop tracer.
func Test_syncProducer_WithoutASampledTracer(t *testing.T) {
	for _, tt := range []struct {
		name string
		ctx  context.Context
	}{
		{"background context", context.Background()},
		{"noop tracer", pinpoint.NewContext(context.Background(), pinpoint.NoopTracer())},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubSyncProducer{}
			p := &syncProducer{SyncProducer: stub, addrs: []string{"broker1:9092"}, ctx: tt.ctx}

			_, _, err := p.SendMessage(&sarama.ProducerMessage{Topic: "widgets"})
			require.NoError(t, err)
			require.NoError(t, p.SendMessages([]*sarama.ProducerMessage{{Topic: "gadgets"}}))

			assert.Len(t, stub.sent, 1, "the message must still be produced")
			assert.Len(t, stub.batches, 1, "the batch must still be produced")
		})
	}
}

// distributedTracingContextWriterProducer is what carries the transaction to
// the consumer; a key it was never given has no value to report.
func Test_distributedTracingContextWriterProducer(t *testing.T) {
	msg := &sarama.ProducerMessage{Topic: "widgets"}
	w := &distributedTracingContextWriterProducer{msg: msg}

	assert.Equal(t, "", w.Get(pinpoint.HeaderTraceId), "an untouched message carries no header")

	w.Set(pinpoint.HeaderTraceId, "txid^1^1")
	w.Set(pinpoint.HeaderSpanId, "7")

	assert.Equal(t, "txid^1^1", w.Get(pinpoint.HeaderTraceId))
	assert.Equal(t, "7", w.Get(pinpoint.HeaderSpanId))
	assert.Equal(t, "", w.Get("X-Absent"))
	assert.Len(t, msg.Headers, 2, "each header must be appended to the message once")
}

// NewSyncProducer reports the broker error rather than handing back a producer
// that cannot send.
func TestNewSyncProducer_ReturnsTheBrokerError(t *testing.T) {
	p, err := NewSyncProducer([]string{"127.0.0.1:1"}, sarama.NewConfig())

	assert.Error(t, err, "a producer for an unreachable broker cannot be created")
	assert.Nil(t, p, "a failed NewSyncProducer must not yield a producer")
}

// An empty batch has nothing to record and must still reach the producer.
func Test_syncProducer_SendMessagesContext_EmptyBatch(t *testing.T) {
	tracer := newCapturingTracer()
	stub := &stubSyncProducer{}
	p := &syncProducer{SyncProducer: stub, addrs: []string{"broker1:9092"}, ctx: context.Background()}

	require.NoError(t, p.SendMessagesContext(pinpoint.NewContext(context.Background(), tracer), nil))

	assert.Empty(t, tracer.events, "an empty batch has no message to record")
	assert.Len(t, stub.batches, 1, "the empty batch must still reach the underlying producer")
}
