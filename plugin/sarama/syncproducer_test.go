package ppsarama

import (
	"context"
	"errors"
	"testing"

	"github.com/Shopify/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

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

	if err != nil {
		t.Fatalf("SendMessageContext() = %v", err)
	}
	if partition != 3 || offset != 42 {
		t.Errorf("SendMessageContext() = %d/%d, want 3/42", partition, offset)
	}
	if len(stub.sent) != 1 || stub.sent[0] != msg {
		t.Fatalf("the underlying producer received %d messages, want the one sent", len(stub.sent))
	}

	writer := &distributedTracingContextWriterProducer{msg}
	for _, key := range []string{
		pinpoint.HeaderTraceId,
		pinpoint.HeaderSpanId,
		pinpoint.HeaderParentSpanId,
		pinpoint.HeaderParentApplicationName,
	} {
		if writer.Get(key) == "" {
			t.Errorf("the produced message is missing the %s header", key)
		}
	}
}

// The span event names the topic and the broker, which is what puts the
// message on the right node of the server map.
func Test_newSyncProducerTracer_RecordsTopicAndBroker(t *testing.T) {
	tracer := newCapturingTracer()
	msg := &sarama.ProducerMessage{Topic: "widgets"}

	newSyncProducerTracer(pinpoint.NewContext(context.Background(), tracer),
		[]string{"broker1:9092", "broker2:9092"}, msg).EndSpanEvent()

	if len(tracer.events) != 1 {
		t.Fatalf("recorded %d span events, want 1", len(tracer.events))
	}
	e := tracer.events[0]
	if e.operation != "sarama.SyncProducer.SendMessage" {
		t.Errorf("operation = %q, want %q", e.operation, "sarama.SyncProducer.SendMessage")
	}
	if e.serviceType != pinpoint.ServiceTypeKafkaClient {
		t.Errorf("service type = %d, want %d", e.serviceType, pinpoint.ServiceTypeKafkaClient)
	}
	if e.destination != "broker1:9092" {
		t.Errorf("destination = %q, want %q", e.destination, "broker1:9092")
	}
	if got := e.annotations[pinpoint.AnnotationKafkaTopic]; got != "widgets" {
		t.Errorf("topic annotation = %q, want %q", got, "widgets")
	}
	if !e.ended {
		t.Error("the span event was left open")
	}
}

// A batch send is one span event per message - each one is a separate record
// on a topic - and all of them have to be closed once the batch returns.
func Test_syncProducer_SendMessagesContext(t *testing.T) {
	tracer := newCapturingTracer()
	stub := &stubSyncProducer{err: errors.New("broker unavailable")}
	p := &syncProducer{SyncProducer: stub, addrs: []string{"broker1:9092"}, ctx: context.Background()}

	msgs := []*sarama.ProducerMessage{{Topic: "widgets"}, {Topic: "gadgets"}}
	err := p.SendMessagesContext(pinpoint.NewContext(context.Background(), tracer), msgs)

	if !errors.Is(err, stub.err) {
		t.Errorf("SendMessagesContext() = %v, want %v", err, stub.err)
	}
	if len(stub.batches) != 1 || len(stub.batches[0]) != 2 {
		t.Fatalf("the underlying producer received %d batches, want one of two messages", len(stub.batches))
	}
	if len(tracer.events) != 2 {
		t.Fatalf("recorded %d span events, want 2", len(tracer.events))
	}
	for i, want := range []string{"widgets", "gadgets"} {
		if got := tracer.events[i].annotations[pinpoint.AnnotationKafkaTopic]; got != want {
			t.Errorf("topic annotation %d = %q, want %q", i, got, want)
		}
		if !tracer.events[i].ended {
			t.Errorf("span event %d was left open", i)
		}
	}
}

// The deprecated WithContext form has to reach the same instrumentation as
// SendMessageContext, through the context stored on the producer.
func Test_syncProducer_WithContext(t *testing.T) {
	tracer := newCapturingTracer()
	stub := &stubSyncProducer{}
	p := &syncProducer{SyncProducer: stub, addrs: []string{"broker1:9092"}, ctx: context.Background()}

	WithContext(pinpoint.NewContext(context.Background(), tracer), p)

	if _, _, err := p.SendMessage(&sarama.ProducerMessage{Topic: "widgets"}); err != nil {
		t.Fatal(err)
	}
	if err := p.SendMessages([]*sarama.ProducerMessage{{Topic: "gadgets"}}); err != nil {
		t.Fatal(err)
	}

	if len(tracer.events) != 2 {
		t.Fatalf("recorded %d span events, want 2", len(tracer.events))
	}
	for i, want := range []string{"widgets", "gadgets"} {
		if got := tracer.events[i].annotations[pinpoint.AnnotationKafkaTopic]; got != want {
			t.Errorf("topic annotation %d = %q, want %q", i, got, want)
		}
	}
}

// A producer used without any pinpoint context still has to produce; the
// wrapper records nothing on a noop tracer.
func Test_syncProducer_WithoutASampledTracer(t *testing.T) {
	stub := &stubSyncProducer{}
	p := &syncProducer{SyncProducer: stub, addrs: []string{"broker1:9092"}, ctx: context.Background()}

	if _, _, err := p.SendMessage(&sarama.ProducerMessage{Topic: "widgets"}); err != nil {
		t.Fatal(err)
	}
	if len(stub.sent) != 1 {
		t.Errorf("the underlying producer received %d messages, want 1", len(stub.sent))
	}
}
