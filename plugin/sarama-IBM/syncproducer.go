package ppsaramaibm

import (
	"context"

	"github.com/IBM/sarama"
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

// distributedTracingContextWriterProducer is the writer the trace context is
// injected into msg through. The caller has already ruled out a nested
// message (isNested), so a Pinpoint context the headers carry is another
// span's, which Set leaves behind as it copies them.
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

// isNested reports whether msg already carries the trace context tracer's own
// span injected - an outer instrumented layer, or the retry pattern re-sending
// a message object the same request sent. The producer then records no span
// event and writes no header: that context travels alone. Another span's
// context - a relay copying a consumed record's headers, which made the
// downstream consumer continue under the relay's own span id - is not, and
// the header writer leaves it behind as it adds this span's.
func isNested(tracer pinpoint.Tracer, msg *sarama.ProducerMessage) bool {
	// A nil message is passed through untouched, as a nested one is: sarama
	// logs and ignores it. Tracing it read msg.Topic and panicked the caller.
	if msg == nil {
		return true
	}
	r := producerHeaderReader{msg}
	return pinpoint.IsInjected(r) && pinpoint.IsInjectedBy(tracer, r)
}

// producerHeaderReader reads a message's record headers as a tracing carrier.
// One pointer wide, so it is handed to an interface without an allocation;
// the writer below, which carries a flag as well, is not.
type producerHeaderReader struct {
	msg *sarama.ProducerMessage
}

// Get reports a record header written with an empty value as present, as the
// consumer reader does: the same message headers are read back here.
func (r producerHeaderReader) Get(key string) (string, bool) {
	for _, h := range r.msg.Headers {
		if string(h.Key) == key {
			return string(h.Value), true
		}
	}
	return "", false
}

// headerKeys is the byte form of every header name this plugin writes, built
// once: Set runs for each of them per message, and []byte(key) allocated the
// same bytes per call. sarama encodes a record header's key and never writes
// into it, so messages share these. Any other key is converted as before.
var headerKeys = func() map[string][]byte {
	m := map[string][]byte{HeaderAsyncSpanId: []byte(HeaderAsyncSpanId)}
	for k := range pinpoint.InjectedHeaders() {
		m[k] = []byte(k)
	}
	return m
}()

func headerKey(key string) []byte {
	if b, ok := headerKeys[key]; ok {
		return b
	}
	return []byte(key)
}

func (m *distributedTracingContextWriterProducer) Set(key string, value string) {
	// The slice is replaced on the first header written, never ahead of it:
	// a message nothing is injected into - no tracer in the context - keeps
	// its own slice, nil included, which a producer below Kafka 0.11 requires.
	// It is grown once for the injected set instead of through the append
	// doublings, into a new array, so no header is written into spare
	// capacity the application's slice may share with another message.
	// Another span's context is not copied: Inject omits a header it has no
	// value for, so a stale one left in place was read as this injection's.
	// Nor is the ack id an earlier release wrote, which no reader takes now.
	if !m.grown {
		const injectedHeaders = 10
		grown := make([]sarama.RecordHeader, 0, len(m.msg.Headers)+injectedHeaders)
		for _, h := range m.msg.Headers {
			if k := string(h.Key); k != HeaderAsyncSpanId && !pinpoint.IsInjectedHeader(k) {
				grown = append(grown, h)
			}
		}
		m.msg.Headers = grown
		m.grown = true
	}
	m.msg.Headers = append(m.msg.Headers, sarama.RecordHeader{
		Key:   headerKey(key),
		Value: []byte(value),
	})
}

// SendMessageContext produces a given message with tracer context.
func (p *syncProducer) SendMessageContext(ctx context.Context, msg *sarama.ProducerMessage) (partition int32, offset int64, err error) {
	// A disabled agent traces nothing and injects nothing - not even the
	// unsampled marker - so the message headers are left untouched.
	if !pinpoint.GetAgent().Enable() {
		return p.SyncProducer.SendMessage(msg)
	}

	tracer := newSyncProducerTracer(ctx, p, msg)
	defer tracer.EndSpanEvent()
	partition, offset, err = p.SyncProducer.SendMessage(msg)
	tracer.SpanEvent().SetError(err)
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

	// One event for the batch call, with the per-message events as its
	// children, each ended as soon as its headers are injected. Keeping every
	// message's event open until the batch returned nested them N deep on the
	// one tracer: past Span.MaxCallStackDepth the rest overflowed, their
	// downstream link fell back to the span-level id, and the survivors
	// rendered as a call chain instead of siblings. The batch event carries
	// the duration and the error; the message events carry topic and headers.
	tracer := pinpoint.FromContext(ctx)
	tracer.NewSpanEvent("sarama.SyncProducer.SendMessages")
	defer tracer.EndSpanEvent()
	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeKafkaClient)
	if len(p.addrs) > 0 {
		se.SetDestination(p.addrs[0])
	}

	for _, msg := range msgs {
		newSyncProducerTracer(ctx, p, msg).EndSpanEvent()
	}

	err := p.SyncProducer.SendMessages(msgs)
	se.SetError(err)
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
	tracer := pinpoint.FromContext(ctx)
	if isNested(tracer, msg) {
		return pinpoint.NoopTracer()
	}

	tracer.NewSpanEvent("sarama.SyncProducer.SendMessage")
	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeKafkaClient)
	se.Annotations().AppendString(pinpoint.AnnotationKafkaTopic, msg.Topic)
	// A wrapped producer (WrapSyncProducer) may come without an address.
	if len(p.addrs) > 0 {
		se.SetDestination(p.addrs[0])
	}

	if !p.noHeaders {
		tracer.Inject(&distributedTracingContextWriterProducer{msg: msg})
	}

	return tracer
}
