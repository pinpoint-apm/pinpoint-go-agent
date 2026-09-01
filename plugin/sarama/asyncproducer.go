package ppsarama

import (
	"context"
	"sync"

	"github.com/Shopify/sarama"
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
	inputDone    chan struct{}
	ackDone      chan struct{}
	drainDone    chan struct{}
	closeOnce    sync.Once
	ctx          context.Context
	spans        map[string]pinpoint.Tracer
	spansLock    sync.Mutex
}

// InputContext sends a given message with tracer context to the input channel of sarama.AsyncProducer.
func (p *asyncProducer) InputContext(ctx context.Context, msg *sarama.ProducerMessage) {
	select {
	case <-p.done:
		return
	default:
	}

	tracer := pinpoint.FromContext(ctx)
	asyncTracer := tracer.NewGoroutineTracer()
	newCtx := pinpoint.NewContext(context.Background(), asyncTracer)
	select {
	case p.inputContext <- &producerMessageContext{msg, newCtx}:
	case <-p.done:
		asyncTracer.EndSpan()
	}
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

// AsyncClose triggers the shutdown and returns without waiting for it, as
// sarama's own does. Handing the underlying AsyncClose to a goroutine is what
// keeps that promise: it has to wait for the input forwarder to stop, or sarama
// would close its input channel under the forwarder's send.
func (p *asyncProducer) AsyncClose() {
	p.closeOnce.Do(func() {
		close(p.done)
		go func() {
			<-p.inputDone
			p.AsyncProducer.AsyncClose()
		}()
	})
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
	if config == nil {
		// sarama.NewAsyncProducer accepts a nil config and substitutes
		// NewConfig() itself. Without the same substitution here, the
		// forwarder's first config read panics, the recover below kills the
		// forwarder, and every later message is drained and silently dropped.
		config = sarama.NewConfig()
	}

	wrapped := &asyncProducer{
		AsyncProducer: producer,
		inputContext:  make(chan *producerMessageContext),
		input:         make(chan *sarama.ProducerMessage),
		successes:     make(chan *sarama.ProducerMessage),
		errors:        make(chan *sarama.ProducerError),
		done:          make(chan struct{}),
		inputDone:     make(chan struct{}),
		ackDone:       make(chan struct{}),
		drainDone:     make(chan struct{}),
		ctx:           context.Background(),
		spans:         make(map[string]pinpoint.Tracer),
	}

	go func() {
		// Keep a closed underlying input from panicking the host application.
		// AsyncClose waits for this goroutine before closing that input itself.
		// Nothing receives from the wrapper's inputs once this returns, so hand
		// them to a drainer before releasing AsyncClose.
		defer func() {
			go drainAsyncProducerInput(wrapped)
			close(wrapped.inputDone)
		}()
		defer func() {
			// A dead forwarder means every later message is silently dropped
			// by the drainer, so its death must never be silent itself.
			if r := recover(); r != nil {
				pinpoint.Log("sarama").Errorf("async producer input forwarder died: %v", r)
			}
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
				if !sendAsyncProducerMessage(producer.Input(), wrapped.done, msgCtx.msg) {
					endAsyncProducerTracer(wrapped, msgCtx.msg, sarama.ErrShuttingDown)
					return
				}
			case msg := <-wrapped.input:
				span := newAsyncProducerTracer(wrapped.ctx, addrs, msg, config)
				saveAsyncProducerTracer(config, wrapped, span)
				if !sendAsyncProducerMessage(producer.Input(), wrapped.done, msg) {
					endAsyncProducerTracer(wrapped, msg, sarama.ErrShuttingDown)
					return
				}
			}
		}
	}()

	go func() {
		// Closed only here, and only after sarama has closed its own pair:
		// AsyncClose guarantees delivery of every in-flight message on
		// Successes/Errors before closing them, and the user is entitled to
		// drain all of it through the wrapper. wrapped.inputContext is never
		// closed - its senders are user goroutines, and closing a
		// channel under a sender panics the send. wrapped.input is closed
		// instead by the drainer below, once no legitimate sender is left.
		defer close(wrapped.ackDone)
		defer close(wrapped.successes)
		defer close(wrapped.errors)
		defer endRemainingAsyncProducerTracers(wrapped)

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

func sendAsyncProducerMessage(input chan<- *sarama.ProducerMessage, done <-chan struct{}, msg *sarama.ProducerMessage) (sent bool) {
	defer func() {
		if recover() != nil {
			sent = false
		}
	}()

	select {
	case input <- msg:
		return true
	case <-done:
		return false
	}
}

// drainAsyncProducerInput releases callers that raced AsyncClose on the
// wrapper's unbuffered inputs. It outlives AsyncClose - which returns as soon
// as the input forwarder is gone - and stops once the wrapper is fully shut
// down, sweeping up whoever parked in the meantime.
//
// It then closes wrapped.input, so that a send issued after shutdown panics
// the caller exactly as raw sarama's own closed input does, instead of parking
// on a channel that nothing will ever receive from again. Racing a send against
// that close is reported by the race detector, again just as it is for raw
// sarama - the send is a programming error either way. wrapped.inputContext
// needs no such close: InputContext gives up on wrapped.done by itself.
func drainAsyncProducerInput(wrapped *asyncProducer) {
	defer close(wrapped.drainDone)

	for {
		select {
		case msgCtx := <-wrapped.inputContext:
			pinpoint.FromContext(msgCtx.ctx).EndSpan()
		case <-wrapped.input:
		case <-wrapped.ackDone:
			for sweepAsyncProducerInput(wrapped) {
			}
			close(wrapped.input)
			return
		}
	}
}

// sweepAsyncProducerInput drops one parked message, reporting whether it found
// one. Both cases of the drainer's select are ready when a sender is parked as
// ackDone closes, and select would pick between them at random.
func sweepAsyncProducerInput(wrapped *asyncProducer) bool {
	select {
	case msgCtx := <-wrapped.inputContext:
		pinpoint.FromContext(msgCtx.ctx).EndSpan()
		return true
	case <-wrapped.input:
		return true
	default:
		return false
	}
}

const HeaderAsyncSpanId = "Pinpoint-AsyncSpanID"

// trackAcks reports whether delivery acks can be relied on to end the tracers.
// Successes alone is not enough: with Return.Errors off, a failed message gets
// no ack at all, and its tracer would sit in the span map until shutdown.
func trackAcks(config *sarama.Config) bool {
	return config.Producer.Return.Successes && config.Producer.Return.Errors
}

func newAsyncProducerTracer(ctx context.Context, addrs []string, msg *sarama.ProducerMessage, config *sarama.Config) pinpoint.Tracer {
	tracer := pinpoint.FromContext(ctx)

	tracer.NewSpanEvent("sarama.AsyncProducer.SendMessage")
	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeKafkaClient)
	se.Annotations().AppendString(pinpoint.AnnotationKafkaTopic, msg.Topic)
	se.SetDestination(addrs[0])

	writer := &distributedTracingContextWriterProducer{msg}
	tracer.Inject(writer)

	if trackAcks(config) && tracer.IsSampled() {
		writer.Set(HeaderAsyncSpanId, tracer.AsyncSpanId())
	}

	return tracer
}

func saveAsyncProducerTracer(config *sarama.Config, wrapped *asyncProducer, span pinpoint.Tracer) {
	if trackAcks(config) && span.IsSampled() {
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
		span, ok := wrapped.spans[id]
		delete(wrapped.spans, id)
		wrapped.spansLock.Unlock()

		if ok {
			if err != nil {
				span.SpanEvent().SetError(err)
			}
			span.EndSpanEvent()
			span.EndSpan()
		}
	}
}

// endRemainingAsyncProducerTracers ends whatever is left once sarama has closed
// its delivery channels. Those messages never got an ack, so their spans record
// the shutdown rather than a send that looks like it succeeded.
func endRemainingAsyncProducerTracers(wrapped *asyncProducer) {
	wrapped.spansLock.Lock()
	spans := wrapped.spans
	wrapped.spans = make(map[string]pinpoint.Tracer)
	wrapped.spansLock.Unlock()

	for _, span := range spans {
		span.SpanEvent().SetError(sarama.ErrShuttingDown)
		span.EndSpanEvent()
		span.EndSpan()
	}
}
