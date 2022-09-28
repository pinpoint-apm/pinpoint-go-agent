package ppsarama

import (
	"context"

	"github.com/Shopify/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

type SyncProducer struct {
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

func (p *SyncProducer) SendMessage(msg *sarama.ProducerMessage) (partition int32, offset int64, err error) {
	span := newProducerTracer(p.ctx, p.addrs, msg)
	partition, offset, err = p.SyncProducer.SendMessage(msg)
	if span != nil {
		span.EndSpanEvent()
	}
	return partition, offset, err
}

func (p *SyncProducer) SendMessages(msgs []*sarama.ProducerMessage) error {
	spans := make([]pinpoint.Tracer, len(msgs))
	for i, msg := range msgs {
		spans[i] = newProducerTracer(p.ctx, p.addrs, msg)
	}

	err := p.SyncProducer.SendMessages(msgs)

	for _, span := range spans {
		if span != nil {
			span.EndSpanEvent()
		}
	}
	return err
}

func (p *SyncProducer) Close() error {
	return p.SyncProducer.Close()
}

func (p *SyncProducer) WithContext(ctx context.Context) {
	p.ctx = ctx
}

func NewSyncProducer(addrs []string, config *sarama.Config) (*SyncProducer, error) {
	producer, err := sarama.NewSyncProducer(addrs, config)
	if err != nil {
		return nil, err
	}

	return &SyncProducer{SyncProducer: producer, addrs: addrs, ctx: context.Background()}, nil
}

func newProducerTracer(ctx context.Context, addrs []string, msg *sarama.ProducerMessage) pinpoint.Tracer {
	tracer := pinpoint.FromContext(ctx)
	if tracer != nil {
		tracer.NewSpanEvent("kafka.produce")
		se := tracer.SpanEvent()
		se.SetServiceType(serviceTypeKafkaClient)
		se.Annotations().AppendString(annotationKafkaTopic, msg.Topic)
		se.SetDestination(addrs[0])

		writer := &distributedTracingContextWriterConsumer{msg}
		tracer.Inject(writer)
	}

	return tracer
}
