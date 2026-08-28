package ppsaramaibm

import (
	"context"
	"sync"

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
	done         chan struct{}
	closeOnce    sync.Once
	ctx          context.Context
	spans        map[string]pinpoint.Tracer
	spansLock    sync.Mutex
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
	p.closeOnce.Do(func() { close(p.done) })
	p.AsyncProducer.AsyncClose()
}

// Close mirrors sarama's own Close - AsyncClose, discard the remaining
// successes, collect the remaining errors - but drains through the wrapper's
// channels. Calling the underlying Close instead would have sarama's internal
// drain and the forwarder below compete for the same delivery events, so some
// spans would never be ended and the returned ProducerErrors would be
// incomplete.
func (p *asyncProducer) Close() error {
	p.AsyncClose()

	go func() {
		for range p.successes {
		}
	}()

	var errs sarama.ProducerErrors
	for e := range p.errors {
		errs = append(errs, e)
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
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

	return wrapAsyncProducer(producer, addrs, config), nil
}

func wrapAsyncProducer(producer sarama.AsyncProducer, addrs []string, config *sarama.Config) *asyncProducer {
	wrapped := &asyncProducer{
		AsyncProducer: producer,
		inputContext:  make(chan *producerMessageContext),
		input:         make(chan *sarama.ProducerMessage),
		successes:     make(chan *sarama.ProducerMessage),
		errors:        make(chan *sarama.ProducerError),
		done:          make(chan struct{}),
		ctx:           context.Background(),
		spans:         make(map[string]pinpoint.Tracer),
	}

	go func() {
		// The send to producer.Input() races AsyncClose closing that channel.
		// Raw sarama panics the sender in that misuse case; a panic here would
		// kill the host application, so drop the message instead.
		defer func() {
			recover()
		}()

		for {
			// The tracer is saved before the send: a broker ack can reach the
			// forwarder below before a save placed after the send, and the
			// span would then never be ended.
			select {
			case <-wrapped.done:
				return
			case msgCtx := <-wrapped.inputContext:
				span := newAsyncProducerTracer(msgCtx.ctx, addrs, msgCtx.msg, config)
				saveAsyncProducerTracer(config, wrapped, span)
				producer.Input() <- msgCtx.msg
			case msg := <-wrapped.input:
				span := newAsyncProducerTracer(wrapped.ctx, addrs, msg, config)
				saveAsyncProducerTracer(config, wrapped, span)
				producer.Input() <- msg
			}
		}
	}()

	go func() {
		// Closed only here, and only after sarama has closed its own pair:
		// AsyncClose guarantees delivery of every in-flight message on
		// Successes/Errors before closing them, and the user is entitled to
		// drain all of it through the wrapper. wrapped.input/inputContext are
		// never closed - their senders are user goroutines, and closing a
		// channel under a sender panics the send.
		defer close(wrapped.successes)
		defer close(wrapped.errors)

		successes, errs := producer.Successes(), producer.Errors()
		for successes != nil || errs != nil {
			select {
			case msg, ok := <-successes:
				if !ok {
					successes = nil
					continue
				}
				endAsyncProducerTracer(wrapped, msg, nil)
				wrapped.successes <- msg
			case e, ok := <-errs:
				if !ok {
					errs = nil
					continue
				}
				endAsyncProducerTracer(wrapped, e.Msg, e.Err)
				wrapped.errors <- e
			}
		}
	}()

	return wrapped
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
	}

	return tracer
}

func saveAsyncProducerTracer(config *sarama.Config, wrapped *asyncProducer, span pinpoint.Tracer) {
	if config.Producer.Return.Successes && span.IsSampled() {
		wrapped.spansLock.Lock()
		defer wrapped.spansLock.Unlock()

		wrapped.spans[span.AsyncSpanId()] = span
	} else {
		span.EndSpanEvent()
		span.EndSpan()
	}
}

func endAsyncProducerTracer(wrapped *asyncProducer, msg *sarama.ProducerMessage, err error) {
	headers := &distributedTracingContextWriterProducer{msg}
	if id := headers.Get(HeaderAsyncSpanId); id != "" {
		wrapped.spansLock.Lock()
		defer wrapped.spansLock.Unlock()

		if span, ok := wrapped.spans[id]; ok {
			if err != nil {
				span.SpanEvent().SetError(err)
			}
			span.EndSpanEvent()
			span.EndSpan()

			delete(wrapped.spans, id)
		}
	}
}
