package ppsaramaibm

import (
	"context"
	"github.com/IBM/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

type producerMessageContext struct {
	msg *sarama.ProducerMessage
	ctx context.Context
}

// AsyncProducer wraps the sarama.AsyncProducer and provides additional function InputContext for trace.
type AsyncProducer interface {
	sarama.AsyncProducer
	InputContext(ctx context.Context, msg *sarama.ProducerMessage)
}

type asyncProducer struct {
	sarama.AsyncProducer
	inputContext chan *producerMessageContext
	input        chan *sarama.ProducerMessage
	successes    chan *sarama.ProducerMessage
	errors       chan *sarama.ProducerError
	ctx          context.Context
}

// InputContext sends a given message with tracer context to the input channel of sarama.AsyncProducer.
func (p *asyncProducer) InputContext(ctx context.Context, msg *sarama.ProducerMessage) {
	tracer := pinpoint.FromContext(ctx)
	newCtx := pinpoint.NewContext(context.Background(), tracer.NewGoroutineTracer())
	p.inputContext <- &producerMessageContext{msg, newCtx}
}

// Input returns the input channel of sarama.AsyncProducer. For trace, WithContext should be called first.
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

// WithContext is deprecated and not thread-safe. Use InputContext.
// WithContext passes the context to the provided producer.
// It is possible to trace only when the given context contains a pinpoint.Tracer.
func (p *asyncProducer) WithContext(ctx context.Context) {
	tracer := pinpoint.FromContext(ctx)
	p.ctx = pinpoint.NewContext(context.Background(), tracer.NewGoroutineTracer())
}

// NewAsyncProducer wraps sarama.NewAsyncProducer and returns a AsyncProducer ready to instrument.
// It requires the underlying sarama Config.Producer.Return.Successes,
// so we can know whether successes will be returned.
func NewAsyncProducer(addrs []string, config *sarama.Config) (AsyncProducer, error) {
	producer, err := sarama.NewAsyncProducer(addrs, config)
	if err != nil {
		return nil, err
	}

	wrapped := &asyncProducer{
		AsyncProducer: producer,
		inputContext:  make(chan *producerMessageContext),
		input:         make(chan *sarama.ProducerMessage),
		successes:     make(chan *sarama.ProducerMessage),
		errors:        make(chan *sarama.ProducerError),
		ctx:           context.Background(),
	}

	go func() {
		spans := make(map[string]pinpoint.Tracer)
		defer close(wrapped.successes)
		defer close(wrapped.errors)

		for {
			select {
			case msgCtx := <-wrapped.inputContext:
				span := newAsyncProducerTracer(msgCtx.ctx, addrs, msgCtx.msg, config)
				producer.Input() <- msgCtx.msg
				saveAsyncProducerTracer(config, spans, span)

			case msg := <-wrapped.input:
				span := newAsyncProducerTracer(wrapped.ctx, addrs, msg, config)
				producer.Input() <- msg
				saveAsyncProducerTracer(config, spans, span)

			case msg, ok := <-producer.Successes():
				if !ok {
					return // producer was closed, so exit
				}
				endAsyncProducerTracer(spans, msg, nil)
				wrapped.successes <- msg

			case e, ok := <-producer.Errors():
				if !ok {
					return // producer was closed
				}
				endAsyncProducerTracer(spans, e.Msg, e.Err)
				wrapped.errors <- e
			}
		}
	}()

	return wrapped, nil
}

const HeaderAsyncSpanId = "Pinpoint-AsyncSpanID"

func newAsyncProducerTracer(ctx context.Context, addrs []string, msg *sarama.ProducerMessage, config *sarama.Config) pinpoint.Tracer {
	tracer := pinpoint.FromContext(ctx)

	tracer.NewSpanEvent("sarama.AsyncProducer.SendMessage")
	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeKafkaClient)
	se.Annotations().AppendString(pinpoint.AnnotationKafkaTopic, msg.Topic)
	se.SetDestination(addrs[0])

	writer := &distributedTracingContextWriterProducer{msg}
	tracer.Inject(writer)

	if config.Producer.Return.Successes && tracer.IsSampled() {
		writer.Set(HeaderAsyncSpanId, tracer.AsyncSpanId())
		//fmt.Printf("Set HeaderAsyncSpanId :%s, topic : %s\n", tracer.AsyncSpanId(), msg.Topic)
	}

	return tracer
}

func saveAsyncProducerTracer(config *sarama.Config, spans map[string]pinpoint.Tracer, span pinpoint.Tracer) {
	if config.Producer.Return.Successes && span.IsSampled() {
		spans[span.AsyncSpanId()] = span
	} else {
		span.EndSpanEvent()
		span.EndSpan()
	}
}

func endAsyncProducerTracer(spans map[string]pinpoint.Tracer, msg *sarama.ProducerMessage, err error) {
	headers := &distributedTracingContextWriterProducer{msg}
	if id := headers.Get(HeaderAsyncSpanId); id != "" {
		if span, ok := spans[id]; ok {
			//fmt.Printf("Get HeaderAsyncSpanId :%s, topic : %s\n", id, msg.Topic)
			if err != nil {
				span.SpanEvent().SetError(err)
			}
			span.EndSpanEvent()
			span.EndSpan()

			delete(spans, id)
		}
	}
}
