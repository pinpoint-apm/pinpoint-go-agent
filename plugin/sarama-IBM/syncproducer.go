package ppsaramaibm

import (
	"bytes"
	"context"
	"slices"

	"github.com/IBM/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent"
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
	ctx   context.Context
}

type distributedTracingContextWriterProducer struct {
	msg *sarama.ProducerMessage
}

var pinpointHeaderPrefix = []byte("Pinpoint-")

// newProducerHeaderWriter prepares msg for injection by removing the headers a
// previous injection left, so Set is a plain append and the header slice is
// grown once instead of through the append doublings.
//
// The retry pattern re-sends the same message object, and the leftovers cannot
// simply be appended beside: Get returns the first match, so the retry's ack
// would be looked up under the stale async id - its tracer sitting in the span
// map until producer shutdown - and the message would grow one full
// trace-header set per attempt. Overwriting them in place is not enough
// either, because Inject writes no header for a value it does not have: a
// stale Pinpoint-Host would then survive as this injection's own, naming a
// destination this send never contacted.
func newProducerHeaderWriter(msg *sarama.ProducerMessage) *distributedTracingContextWriterProducer {
	msg.Headers = slices.DeleteFunc(msg.Headers, func(h sarama.RecordHeader) bool {
		return bytes.HasPrefix(h.Key, pinpointHeaderPrefix)
	})
	const injectedHeaders = 10
	if cap(msg.Headers)-len(msg.Headers) < injectedHeaders {
		grown := make([]sarama.RecordHeader, len(msg.Headers), len(msg.Headers)+injectedHeaders)
		copy(grown, msg.Headers)
		msg.Headers = grown
	}
	return &distributedTracingContextWriterProducer{msg: msg}
}

func (m *distributedTracingContextWriterProducer) Set(key string, value string) {
	m.msg.Headers = append(m.msg.Headers, sarama.RecordHeader{
		Key:   []byte(key),
		Value: []byte(value),
	})
}

func (m *distributedTracingContextWriterProducer) Get(key string) string {
	for _, h := range m.msg.Headers {
		if string(h.Key) == key {
			return string(h.Value)
		}
	}
	return ""
}

// SendMessageContext produces a given message with tracer context.
func (p *syncProducer) SendMessageContext(ctx context.Context, msg *sarama.ProducerMessage) (partition int32, offset int64, err error) {
	// A disabled agent traces nothing and injects nothing - not even the
	// unsampled marker - so the message headers are left untouched.
	if !pinpoint.GetAgent().Enable() {
		return p.SyncProducer.SendMessage(msg)
	}

	defer newSyncProducerTracer(ctx, p.addrs, msg).EndSpanEvent()
	partition, offset, err = p.SyncProducer.SendMessage(msg)
	return partition, offset, err
}

// SendMessage produces a given message. For trace, WithContext should be called first.
func (p *syncProducer) SendMessage(msg *sarama.ProducerMessage) (partition int32, offset int64, err error) {
	return p.SendMessageContext(p.ctx, msg)
}

// SendMessagesContext produces a given set of messages with tracer context.
func (p *syncProducer) SendMessagesContext(ctx context.Context, msgs []*sarama.ProducerMessage) error {
	if !pinpoint.GetAgent().Enable() {
		return p.SyncProducer.SendMessages(msgs)
	}

	spans := make([]pinpoint.Tracer, len(msgs))
	for i, msg := range msgs {
		spans[i] = newSyncProducerTracer(ctx, p.addrs, msg)
	}

	err := p.SyncProducer.SendMessages(msgs)

	for _, span := range spans {
		span.EndSpanEvent()
	}
	return err
}

// SendMessages produces a given set of messages. For trace, WithContext should be called first.
func (p *syncProducer) SendMessages(msgs []*sarama.ProducerMessage) error {
	return p.SendMessagesContext(p.ctx, msgs)
}

func (p *syncProducer) Close() error {
	return p.SyncProducer.Close()
}

// WithContext is deprecated and not thread-safe. Use SendMessageContext.
// WithContext passes the context to the provided producer.
// It is possible to trace only when the given context contains a pinpoint.Tracer.
func (p *syncProducer) WithContext(ctx context.Context) {
	p.ctx = ctx
}

// NewSyncProducer wraps sarama.NewSyncProducer and returns a sarama.SyncProducer ready to instrument.
func NewSyncProducer(addrs []string, config *sarama.Config) (SyncProducer, error) {
	producer, err := sarama.NewSyncProducer(addrs, config)
	if err != nil {
		return nil, err
	}

	return &syncProducer{SyncProducer: producer, addrs: addrs, ctx: context.Background()}, nil
}

func newSyncProducerTracer(ctx context.Context, addrs []string, msg *sarama.ProducerMessage) pinpoint.Tracer {
	tracer := pinpoint.FromContext(ctx)

	tracer.NewSpanEvent("sarama.SyncProducer.SendMessage")
	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeKafkaClient)
	se.Annotations().AppendString(pinpoint.AnnotationKafkaTopic, msg.Topic)
	se.SetDestination(addrs[0])

	tracer.Inject(newProducerHeaderWriter(msg))

	return tracer
}

// WithContext is deprecated and not thread-safe.
// WithContext passes the context to the provided producer.
// It is possible to trace only when the given context contains a pinpoint.Tracer.
func WithContext(ctx context.Context, producer interface{}) {
	if p, ok := producer.(*syncProducer); ok {
		p.WithContext(ctx)
	} else if p, ok := producer.(*asyncProducer); ok {
		p.WithContext(ctx)
	}
}
