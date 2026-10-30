package ppsaramaibm

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"

	"github.com/IBM/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
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

// A message carrying a context no transaction of this tracer's can replace -
// here no tracer at all - is sent as it is: no span event is recorded and no
// header written, so the message keeps exactly the headers it arrived with.
func Test_newSyncProducerTracer_NestedMessageIsNotTraced(t *testing.T) {
	msg := &sarama.ProducerMessage{
		Topic: "topic",
		Headers: []sarama.RecordHeader{
			{Key: []byte("x-app"), Value: []byte("keep-me")},
			{Key: []byte(pinpoint.HeaderTraceId), Value: []byte("first^1^1")},
			{Key: []byte(pinpoint.HeaderHost), Value: []byte("broker-of-the-first-attempt")},
		},
	}
	before := append([]sarama.RecordHeader(nil), msg.Headers...)

	tracer := newSyncProducerTracer(context.Background(), &syncProducer{addrs: []string{"broker:9092"}}, msg)
	tracer.EndSpanEvent()

	assert.Equal(t, before, msg.Headers, "a nested message's headers are left untouched")
	assert.False(t, tracer.IsSampled(), "a nested message gets a noop tracer")
}

// A message carrying another span's context - a relay copying the headers of
// the record it consumed, whose span id is the relay's own - gets this span's
// context in place of that one, stale keys and an earlier release's ack id
// included, and keeps the application's own headers: sent on as it was, the
// downstream consumer continued under the relay's span id.
func Test_newSyncProducerTracer_ForwardedContextIsReplaced(t *testing.T) {
	pptest.StartAgent(t)
	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/relay")
	defer tracer.EndSpan()
	defer tracer.NewSpanEvent("relay").EndSpanEvent()
	spanId := strconv.FormatInt(tracer.SpanId(), 10)

	consumed := []sarama.RecordHeader{
		{Key: []byte("x-app"), Value: []byte("keep-me")},
		{Key: []byte(pinpoint.HeaderTraceId), Value: []byte(tracer.TransactionId().String())},
		{Key: []byte(pinpoint.HeaderSpanId), Value: []byte(spanId)},
		{Key: []byte(pinpoint.HeaderParentSpanId), Value: []byte("12345")},
		{Key: []byte(pinpoint.HeaderHost), Value: []byte("stale:9092")},
		{Key: []byte(HeaderAsyncSpanId), Value: []byte("1^2^3")},
	}
	msg := &sarama.ProducerMessage{Topic: "topic", Headers: append([]sarama.RecordHeader(nil), consumed...)}

	newSyncProducerTracer(pinpoint.NewContext(context.Background(), tracer), &syncProducer{addrs: []string{"broker:9092"}}, msg).EndSpanEvent()

	got := map[string][]string{}
	for _, h := range msg.Headers {
		got[string(h.Key)] = append(got[string(h.Key)], string(h.Value))
	}
	assert.Equal(t, []string{"keep-me"}, got["x-app"])
	assert.Equal(t, []string{spanId}, got[pinpoint.HeaderParentSpanId], "the consumer's parent must be this span")
	assert.NotEqual(t, []string{spanId}, got[pinpoint.HeaderSpanId], "the consumer must not take this span's id")
	assert.Equal(t, []string{"broker:9092"}, got[pinpoint.HeaderHost], "a stale header was left in place")
	assert.Empty(t, got[HeaderAsyncSpanId], "the earlier release's ack id was left in place")
	for key, values := range got {
		assert.Len(t, values, 1, "%s carries two values", key)
	}
	assert.Equal(t, "stale:9092", string(consumed[4].Value), "the consumed headers must stay as they were")
}

// isNested sends as it is a nil message and one carrying the context tracer's
// own span injected, and nothing else.
func Test_isNested(t *testing.T) {
	pptest.StartAgent(t)
	hdr := func(k string) []sarama.RecordHeader {
		return []sarama.RecordHeader{{Key: []byte(k), Value: []byte("v")}}
	}
	noop := pinpoint.NoopTracer()
	assert.True(t, isNested(noop, nil))
	assert.False(t, isNested(noop, &sarama.ProducerMessage{}))
	assert.False(t, isNested(noop, &sarama.ProducerMessage{Headers: hdr("x-app")}))
	assert.True(t, isNested(noop, &sarama.ProducerMessage{Headers: hdr(pinpoint.HeaderTraceId)}), "a no-op tracer has no context to replace it with")
	assert.True(t, isNested(noop, &sarama.ProducerMessage{Headers: hdr(pinpoint.HeaderSampled)}), "an unsampled context is a context too")
	assert.False(t, isNested(noop, &sarama.ProducerMessage{Headers: hdr(HeaderAsyncSpanId)}), "an earlier release's ack id alone is no context")

	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/produce")
	defer tracer.EndSpan()
	defer tracer.NewSpanEvent("produce").EndSpanEvent()
	own := &sarama.ProducerMessage{}
	tracer.Inject(&distributedTracingContextWriterProducer{msg: own})
	assert.True(t, isNested(tracer, own), "the span's own context")
	assert.False(t, isNested(tracer, &sarama.ProducerMessage{Headers: hdr(pinpoint.HeaderTraceId)}), "another span's context")
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

// capturingTracer records what the producer wrapper puts on a span event. A
// real tracer's recorders are write-only, so this stands in for one. Span
// events nest like the real tracer's - a batch send opens all of them before
// closing any - so open events are kept on a stack.
type capturingTracer struct {
	pinpoint.Tracer
	events []*capturedEvent
	open   []*capturedEvent
	// maxOpen is the deepest the event stack got: what the call stack depth
	// limit of a real span is compared against.
	maxOpen int
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
	t.maxOpen = max(t.maxOpen, len(t.open))
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
	err         error
}

func (e *capturedEvent) SetServiceType(typ int32) { e.serviceType = typ }
func (e *capturedEvent) SetDestination(id string) { e.destination = id }
func (e *capturedEvent) SetError(err error, _ ...string) {
	if err != nil {
		e.err = err
	}
}

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
	pptest.StartAgent(t)

	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/produce")
	defer tracer.EndSpan()

	stub := &stubSyncProducer{}
	p := &syncProducer{SyncProducer: stub, addrs: []string{"broker1:9092"}}
	msg := &sarama.ProducerMessage{Topic: "widgets"}

	partition, offset, err := p.SendMessageContext(pinpoint.NewContext(context.Background(), tracer), msg)

	require.NoError(t, err)
	assert.Equal(t, int32(3), partition, "the underlying producer's partition must come back unchanged")
	assert.Equal(t, int64(42), offset, "the underlying producer's offset must come back unchanged")
	require.Len(t, stub.sent, 1)
	assert.Same(t, msg, stub.sent[0], "the underlying producer received a different message")

	for _, key := range pinpointHeaders {
		v, ok := producerHeaderReader{msg}.Get(key)
		assert.True(t, ok, "the produced message is missing the %s header", key)
		assert.NotEmpty(t, v, "the %s header is empty", key)
	}
	tid, _ := producerHeaderReader{msg}.Get(pinpoint.HeaderTraceId)
	assert.Equal(t, tracer.TransactionId().String(), tid)
}

// The span event names the topic and the broker, which is what puts the
// message on the right node of the server map.
func Test_newSyncProducerTracer_RecordsTopicAndBroker(t *testing.T) {
	tracer := newCapturingTracer()
	msg := &sarama.ProducerMessage{Topic: "widgets"}

	newSyncProducerTracer(pinpoint.NewContext(context.Background(), tracer),
		&syncProducer{addrs: []string{"broker1:9092", "broker2:9092"}}, msg).EndSpanEvent()

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
	pptest.StartAgent(t)
	tracer := newCapturingTracer()
	stub := &stubSyncProducer{err: errors.New("broker unavailable")}
	p := &syncProducer{SyncProducer: stub, addrs: []string{"broker1:9092"}}

	msgs := []*sarama.ProducerMessage{{Topic: "widgets"}, {Topic: "gadgets"}}
	err := p.SendMessagesContext(pinpoint.NewContext(context.Background(), tracer), msgs)

	assert.ErrorIs(t, err, stub.err, "the batch error must come back unchanged")
	require.Len(t, stub.batches, 1)
	assert.Len(t, stub.batches[0], 2, "the whole batch must reach the underlying producer")

	require.Len(t, tracer.events, 3, "one event for the batch call, then one per message")
	batch := tracer.events[0]
	assert.Equal(t, "sarama.SyncProducer.SendMessages", batch.operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeKafkaClient), batch.serviceType)
	assert.Equal(t, "broker1:9092", batch.destination)
	assert.ErrorIs(t, batch.err, stub.err, "the batch event must record the send error")
	for i, want := range []string{"widgets", "gadgets"} {
		assert.Equal(t, want, tracer.events[i+1].annotations[pinpoint.AnnotationKafkaTopic],
			"topic annotation %d", i)
	}
	for i, e := range tracer.events {
		assert.True(t, e.ended, "span event %d was left open", i)
	}
	assert.Equal(t, 2, tracer.maxOpen, "message events are siblings under the batch event, not nested")
}

// A batch larger than Span.MaxCallStackDepth (64 by default) must not nest one
// event per message: that overflowed the call stack, dropped the events past
// the limit and lost their downstream link.
func Test_syncProducer_SendMessagesContext_LargeBatchStaysTwoDeep(t *testing.T) {
	pptest.StartAgent(t)
	tracer := newCapturingTracer()
	p := &syncProducer{SyncProducer: &stubSyncProducer{}, addrs: []string{"broker1:9092"}}

	msgs := make([]*sarama.ProducerMessage, 200)
	for i := range msgs {
		msgs[i] = &sarama.ProducerMessage{Topic: "widgets"}
	}
	require.NoError(t, p.SendMessagesContext(pinpoint.NewContext(context.Background(), tracer), msgs))

	assert.Len(t, tracer.events, 201)
	assert.Equal(t, 2, tracer.maxOpen, "a batch of any size keeps the event stack two deep")
	assert.Empty(t, tracer.open, "every event was ended")
}

// A failed single send is an error on its event, not a green event.
func Test_syncProducer_SendMessageContext_RecordsTheError(t *testing.T) {
	pptest.StartAgent(t)
	tracer := newCapturingTracer()
	stub := &stubSyncProducer{err: errors.New("broker unavailable")}
	p := &syncProducer{SyncProducer: stub, addrs: []string{"broker1:9092"}}

	_, _, err := p.SendMessageContext(pinpoint.NewContext(context.Background(), tracer), &sarama.ProducerMessage{Topic: "widgets"})

	assert.ErrorIs(t, err, stub.err)
	require.Len(t, tracer.events, 1)
	assert.ErrorIs(t, tracer.events[0].err, stub.err, "the send error must be recorded on the event")
	assert.True(t, tracer.events[0].ended)
}

// A producer used without any pinpoint context still has to produce; the
// wrapper records nothing on a noop tracer.
func Test_syncProducer_WithoutASampledTracer(t *testing.T) {
	stub := &stubSyncProducer{}
	p := &syncProducer{SyncProducer: stub, addrs: []string{"broker1:9092"}}
	ctx := context.Background()

	_, _, err := p.SendMessageContext(ctx, &sarama.ProducerMessage{Topic: "widgets"})
	require.NoError(t, err)
	require.NoError(t, p.SendMessagesContext(ctx, []*sarama.ProducerMessage{{Topic: "gadgets"}}))

	assert.Len(t, stub.sent, 1, "the message must still be produced")
	assert.Len(t, stub.batches, 1, "the batch must still be produced")
}

// distributedTracingContextWriterProducer is what carries the transaction to
// the consumer; a key it was never given has no value to report.
func Test_distributedTracingContextWriterProducer(t *testing.T) {
	msg := &sarama.ProducerMessage{Topic: "widgets"}
	w := &distributedTracingContextWriterProducer{msg: msg}

	_, ok := producerHeaderReader{msg}.Get(pinpoint.HeaderTraceId)
	assert.False(t, ok, "an untouched message carries no header")

	w.Set(pinpoint.HeaderTraceId, "txid^1^1")
	w.Set(pinpoint.HeaderSpanId, "7")

	v, ok := producerHeaderReader{msg}.Get(pinpoint.HeaderTraceId)
	assert.True(t, ok)
	assert.Equal(t, "txid^1^1", v)
	v, ok = producerHeaderReader{msg}.Get(pinpoint.HeaderSpanId)
	assert.True(t, ok)
	assert.Equal(t, "7", v)
	_, ok = producerHeaderReader{msg}.Get("X-Absent")
	assert.False(t, ok)
	assert.Len(t, msg.Headers, 2, "each header must be appended to the message once")

	// A header written with an empty value is present, unlike one never
	// written - the value alone reports both as "".
	w.Set(pinpoint.HeaderParentSpanId, "")
	v, ok = producerHeaderReader{msg}.Get(pinpoint.HeaderParentSpanId)
	assert.True(t, ok, "a header written with an empty value is present")
	assert.Equal(t, "", v)
}

// NewSyncProducer reports the broker error rather than handing back a producer
// that cannot send.
func TestNewSyncProducer_ReturnsTheBrokerError(t *testing.T) {
	// No metadata retries: the default policy spends seconds backing off
	// against a broker that is never coming up.
	config := sarama.NewConfig()
	config.Metadata.Retry.Max = 0

	p, err := NewSyncProducer([]string{closedAddr(t)}, config)

	assert.Error(t, err, "a producer for an unreachable broker cannot be created")
	assert.Nil(t, p, "a failed NewSyncProducer must not yield a producer")
}

// closedAddr returns a loopback address with nothing listening on it: the port
// is bound and released, so a connection is refused right away. A hard-coded
// port like :1 only works while nothing serves it and while the sandbox
// allows the dial at all, which is not something a test should rest on.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}
