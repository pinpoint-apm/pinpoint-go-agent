package ppsarama

import (
	"context"
	"github.com/Shopify/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

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

type distributedTracingContextWriterConsumer struct {
	msg *sarama.ProducerMessage
}

func (m *distributedTracingContextWriterConsumer) Set(key string, value string) {
	m.msg.Headers = append(m.msg.Headers, sarama.RecordHeader{
		Key:   []byte(key),
		Value: []byte(value),
	})
}

// SendMessageContext produces a given message with the context.
// It is possible to trace only when the given context contains a pinpoint.Tracer.
func (p *syncProducer) SendMessageContext(ctx context.Context, msg *sarama.ProducerMessage) (partition int32, offset int64, err error) {
	defer newProducerTracer(ctx, p.addrs, msg).EndSpanEvent()
	partition, offset, err = p.SyncProducer.SendMessage(msg)
	return partition, offset, err
}

func (p *syncProducer) SendMessage(msg *sarama.ProducerMessage) (partition int32, offset int64, err error) {
	return p.SendMessageContext(p.ctx, msg)
}

// SendMessagesContext produces a given set of messages with the context.
// It is possible to trace only when the given context contains a pinpoint.Tracer.
func (p *syncProducer) SendMessagesContext(ctx context.Context, msgs []*sarama.ProducerMessage) error {
	spans := make([]pinpoint.Tracer, len(msgs))
	for i, msg := range msgs {
		spans[i] = newProducerTracer(ctx, p.addrs, msg)
	}

	err := p.SyncProducer.SendMessages(msgs)

	for _, span := range spans {
		span.EndSpanEvent()
	}
	return err
}

func (p *syncProducer) SendMessages(msgs []*sarama.ProducerMessage) error {
	return p.SendMessagesContext(p.ctx, msgs)
}

func (p *syncProducer) Close() error {
	return p.SyncProducer.Close()
}

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

func newProducerTracer(ctx context.Context, addrs []string, msg *sarama.ProducerMessage) pinpoint.Tracer {
	tracer := pinpoint.FromContext(ctx)

	tracer.NewSpanEvent("sarama.SendMessage")
	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeKafkaClient)
	se.Annotations().AppendString(pinpoint.AnnotationKafkaTopic, msg.Topic)
	se.SetDestination(addrs[0])

	writer := &distributedTracingContextWriterConsumer{msg}
	tracer.Inject(writer)

	return tracer
}

// WithContext passes the context to the provided producer.
// It is possible to trace only when the given context contains a pinpoint.Tracer.
func WithContext(ctx context.Context, producer interface{}) {
	if p, ok := producer.(*syncProducer); ok {
		p.WithContext(ctx)
	} else if p, ok := producer.(*asyncProducer); ok {
		p.WithContext(ctx)
	}
}
