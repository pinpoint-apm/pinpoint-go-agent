package ppsarama

import (
	"context"

	"github.com/Shopify/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

// SyncProducer wraps the sarama.SyncProducer
// and provides additional functions (SendMessageContext, SendMessagesContext) for trace.
type SyncProducer interface {
	sarama.SyncProducer
	SendMessageContext(ctx context.Context, msg *sarama.ProducerMessage) (partition int32, offset int64, err error)
	SendMessagesContext(ctx context.Context, msgs []*sarama.ProducerMessage) error
}

type syncProducer struct {
	sarama.SyncProducer
	addrs []string
	// noHeaders is set for a producer whose Kafka version predates record
	// headers (headersSupported).
	noHeaders bool
}

type distributedTracingContextWriterProducer struct {
	msg   *sarama.ProducerMessage
	grown bool
}

// headersSupported reports whether a producer configured with config can send
// record headers. Below Kafka 0.11 sarama rejects every message that carries
// any - and 0.8.2 is Config.Version's default in older sarama releases - so
// such a producer propagates no trace context rather than losing every
// message.
func headersSupported(config *sarama.Config) bool {
	return config.Version.IsAtLeast(sarama.V0_11_0_0)
}

// isNested reports whether msg already carries a Pinpoint trace context - an
// outer instrumented layer, or the retry pattern re-sending the same message
// object. The producer then records no span event and writes no header: the
// context already present travels alone. The async producer's own ack id counts
// too, since nothing but a previous injection of this plugin writes it.
func isNested(msg *sarama.ProducerMessage) bool {
	// A nil message is passed through untouched, as a nested one is: sarama
	// logs and ignores it. Tracing it read msg.Topic and panicked - in the
	// caller for InputContext, and in the input forwarder for Input, whose
	// death then dropped every later message in silence.
	if msg == nil {
		return true
	}
	w := distributedTracingContextWriterProducer{msg: msg}
	// Presence, not a non-empty value: a header this plugin injected is a
	// context already present even if the value it carries is empty.
	for _, key := range []string{pinpoint.HeaderTraceId, pinpoint.HeaderSampled, HeaderAsyncSpanId} {
		if _, ok := w.Get(key); ok {
			return true
		}
	}
	return false
}

// newProducerHeaderWriter returns the writer the trace context is injected into
// msg through. The caller has already ruled out a nested message (isNested),
// so Set is a plain append onto headers that carry no Pinpoint context yet.
func newProducerHeaderWriter(msg *sarama.ProducerMessage) *distributedTracingContextWriterProducer {
	return &distributedTracingContextWriterProducer{msg: msg}
}

func (m *distributedTracingContextWriterProducer) Set(key string, value string) {
	// The slice is replaced on the first header written, never ahead of it:
	// a message nothing is injected into - no tracer in the context - keeps
	// its own slice, nil included, which a producer below Kafka 0.11 requires.
	// It is grown once for the injected set instead of through the append
	// doublings, into a new array, so no header is written into spare
	// capacity the application's slice may share with another message.
	if !m.grown {
		const injectedHeaders = 10
		grown := make([]sarama.RecordHeader, len(m.msg.Headers), len(m.msg.Headers)+injectedHeaders)
		copy(grown, m.msg.Headers)
		m.msg.Headers = grown
		m.grown = true
	}
	m.msg.Headers = append(m.msg.Headers, sarama.RecordHeader{
		Key:   []byte(key),
		Value: []byte(value),
	})
}

// Get reports a record header written with an empty value as present, as the
// consumer reader does: the same message headers are read back here.
func (m *distributedTracingContextWriterProducer) Get(key string) (string, bool) {
	if m.msg == nil {
		return "", false
	}
	for _, h := range m.msg.Headers {
		if string(h.Key) == key {
			return string(h.Value), true
		}
	}
	return "", false
}

// SendMessageContext produces a given message with tracer context.
func (p *syncProducer) SendMessageContext(ctx context.Context, msg *sarama.ProducerMessage) (partition int32, offset int64, err error) {
	// A disabled agent traces nothing and injects nothing - not even the
	// unsampled marker - so the message headers are left untouched.
	if !pinpoint.GetAgent().Enable() {
		return p.SyncProducer.SendMessage(msg)
	}

	defer newSyncProducerTracer(ctx, p, msg).EndSpanEvent()
	partition, offset, err = p.SyncProducer.SendMessage(msg)
	return partition, offset, err
}

// SendMessage produces a given message without tracing. Use SendMessageContext.
func (p *syncProducer) SendMessage(msg *sarama.ProducerMessage) (partition int32, offset int64, err error) {
	return p.SyncProducer.SendMessage(msg)
}

// SendMessagesContext produces a given set of messages with tracer context.
func (p *syncProducer) SendMessagesContext(ctx context.Context, msgs []*sarama.ProducerMessage) error {
	if !pinpoint.GetAgent().Enable() {
		return p.SyncProducer.SendMessages(msgs)
	}

	spans := make([]pinpoint.Tracer, len(msgs))
	for i, msg := range msgs {
		spans[i] = newSyncProducerTracer(ctx, p, msg)
	}

	err := p.SyncProducer.SendMessages(msgs)

	for _, span := range spans {
		span.EndSpanEvent()
	}
	return err
}

// SendMessages produces a given set of messages without tracing. Use SendMessagesContext.
func (p *syncProducer) SendMessages(msgs []*sarama.ProducerMessage) error {
	return p.SyncProducer.SendMessages(msgs)
}

// NewSyncProducer wraps sarama.NewSyncProducer and returns a sarama.SyncProducer ready to instrument.
func NewSyncProducer(addrs []string, config *sarama.Config) (SyncProducer, error) {
	producer, err := sarama.NewSyncProducer(addrs, config)
	if err != nil {
		return nil, err
	}

	return WrapSyncProducer(producer, addrs, config), nil
}

// WrapSyncProducer wraps a sarama.SyncProducer created elsewhere the way
// NewSyncProducer wraps the one it creates: SendMessageContext and
// SendMessagesContext trace on the context given; SendMessage and
// SendMessages trace nothing. addrs are the broker
// addresses; the first is the span event's destination. config is the
// producer's configuration (nil means sarama's default): below Kafka 0.11
// (Config.Version) no trace header is written. A producer that is already
// wrapped is returned as it is, and a nil one - what sarama.NewSyncProducer
// returns with its error - as nil. The compile-time instrumentation tool uses
// it from its sarama.NewSyncProducer hook.
func WrapSyncProducer(producer sarama.SyncProducer, addrs []string, config *sarama.Config) SyncProducer {
	if producer == nil {
		return nil
	}
	if p, ok := producer.(*syncProducer); ok {
		return p
	}
	if config == nil {
		config = sarama.NewConfig() // what sarama.NewSyncProducer substitutes for nil
	}
	return &syncProducer{SyncProducer: producer, addrs: addrs, noHeaders: !headersSupported(config)}
}

func newSyncProducerTracer(ctx context.Context, p *syncProducer, msg *sarama.ProducerMessage) pinpoint.Tracer {
	if isNested(msg) {
		return pinpoint.NoopTracer()
	}
	tracer := pinpoint.FromContext(ctx)

	tracer.NewSpanEvent("sarama.SyncProducer.SendMessage")
	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeKafkaClient)
	se.Annotations().AppendString(pinpoint.AnnotationKafkaTopic, msg.Topic)
	// A wrapped producer (WrapSyncProducer) may come without an address.
	if len(p.addrs) > 0 {
		se.SetDestination(p.addrs[0])
	}

	if !p.noHeaders {
		tracer.Inject(newProducerHeaderWriter(msg))
	}

	return tracer
}
