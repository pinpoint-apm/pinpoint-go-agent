package ppsaramaibm

import (
	"context"
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

	writer := &distributedTracingContextWriterProducer{msg}
	tracer.Inject(writer)

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
