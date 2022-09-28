package ppsarama

import (
	"context"
	"github.com/Shopify/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

type AsyncProducer struct {
	original  sarama.AsyncProducer
	input     chan *sarama.ProducerMessage
	successes chan *sarama.ProducerMessage
	errors    chan *sarama.ProducerError
	ctx       context.Context
}

func (p *AsyncProducer) Input() chan<- *sarama.ProducerMessage {
	return p.input
}

func (p *AsyncProducer) Successes() <-chan *sarama.ProducerMessage {
	return p.successes
}

func (p *AsyncProducer) Errors() <-chan *sarama.ProducerError {
	return p.errors
}

func (p *AsyncProducer) AsyncClose() {
	p.original.AsyncClose()
}

func (p *AsyncProducer) Close() error {
	return p.original.Close()
}

func (p *AsyncProducer) WithContext(ctx context.Context) {
	p.ctx = ctx
}

func NewAsyncProducer(addrs []string, config *sarama.Config) (*AsyncProducer, error) {
	producer, err := sarama.NewAsyncProducer(addrs, config)
	if err != nil {
		return nil, err
	}

	wrapped := &AsyncProducer{
		original:  producer,
		input:     make(chan *sarama.ProducerMessage),
		successes: make(chan *sarama.ProducerMessage),
		errors:    make(chan *sarama.ProducerError),
		ctx:       context.Background(),
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
					// if returning successes isn't enabled, we just finish the
					// span right away because there's no way to know when it will
					// be done
					if span != nil {
						span.EndSpanEvent()
					}
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
