package ppsaramaibm

import (
	"context"
	"github.com/IBM/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

type asyncProducer struct {
	sarama.AsyncProducer
	input     chan *sarama.ProducerMessage
	successes chan *sarama.ProducerMessage
	errors    chan *sarama.ProducerError
	ctx       context.Context
}

func (p *asyncProducer) Input() chan<- *sarama.ProducerMessage {
	return p.input
}

func (p *asyncProducer) Successes() <-chan *sarama.ProducerMessage {
	return p.successes
}

func (p *asyncProducer) Errors() <-chan *sarama.ProducerError {
	return p.errors
}

func (p *asyncProducer) AsyncClose() {
	p.AsyncProducer.AsyncClose()
}

func (p *asyncProducer) Close() error {
	return p.AsyncProducer.Close()
}

func (p *asyncProducer) WithContext(ctx context.Context) {
	p.ctx = ctx
}

// NewAsyncProducer wraps sarama.NewAsyncProducer and returns a sarama.AsyncProducer ready to instrument.
func NewAsyncProducer(addrs []string, config *sarama.Config) (sarama.AsyncProducer, error) {
	producer, err := sarama.NewAsyncProducer(addrs, config)
	if err != nil {
		return nil, err
	}

	wrapped := &asyncProducer{
		AsyncProducer: producer,
		input:         make(chan *sarama.ProducerMessage),
		successes:     make(chan *sarama.ProducerMessage),
		errors:        make(chan *sarama.ProducerError),
		ctx:           context.Background(),
	}

	go func() {
		type spanKey struct {
			topic     string
			partition int32
			offset    int64
		}

		spans := make(map[spanKey]pinpoint.Tracer)
		defer close(wrapped.successes)
		defer close(wrapped.errors)

		for {
			select {
			case msg := <-wrapped.input:
				key := spanKey{msg.Topic, msg.Partition, msg.Offset}
				span := newProducerTracer(wrapped.ctx, addrs, msg)
				producer.Input() <- msg
				if config.Producer.Return.Successes {
					spans[key] = span
				} else {
					span.EndSpanEvent()
				}
			case msg, ok := <-producer.Successes():
				if !ok {
					// producer was closed, so exit
					return
				}
				key := spanKey{msg.Topic, msg.Partition, msg.Offset}
				if span, ok := spans[key]; ok {
					delete(spans, key)
					span.EndSpanEvent()
				}
				wrapped.successes <- msg
			case err, ok := <-producer.Errors():
				if !ok {
					// producer was closed
					return
				}
				key := spanKey{err.Msg.Topic, err.Msg.Partition, err.Msg.Offset}
				if span, ok := spans[key]; ok {
					delete(spans, key)
					span.SpanEvent().SetError(err.Err)
					span.EndSpanEvent()
				}
				wrapped.errors <- err
			}
		}
	}()

	return wrapped, nil
}
