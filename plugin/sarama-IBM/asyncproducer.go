package ppsaramaibm

import (
	"context"
	"sync"

	"github.com/IBM/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

// AsyncProducer wraps the sarama.AsyncProducer and provides additional function InputContext for trace.
type AsyncProducer interface {
	sarama.AsyncProducer
	InputContext(ctx context.Context, msg *sarama.ProducerMessage)
}

// asyncProducer sends on sarama's own input: Input, CommitTxn and AbortTxn are
// the embedded producer's. sarama's unbuffered input is the one FIFO that keeps
// one goroutine's messages in order across Input and InputContext, and queues
// a transaction's end marker behind the messages sent before it. The wrapper
// owns only the acks, which end the message spans on their way to Successes
// and Errors.
type asyncProducer struct {
	sarama.AsyncProducer
	addrs     []string
	config    *sarama.Config
	successes chan *sarama.ProducerMessage
	errors    chan *sarama.ProducerError
	closeOnce sync.Once
	// spans holds the tracer of every message waiting for its ack, under the
	// message itself: sarama hands the same *ProducerMessage back on
	// Successes and Errors, so the ack needs no id. One message object in
	// flight twice at once keeps only the later span; the retry pattern that
	// re-sends an object does so after its ack, and is nested anyway.
	spans     map[*sarama.ProducerMessage]pinpoint.Tracer
	spansLock sync.Mutex
}

// InputContext sends a given message with tracer context to the input channel of sarama.AsyncProducer.
//
// It sends on the channel Input() returns, so one goroutine's messages keep
// their order across the two. The send does not watch ctx.Done(): like
// Input(), it blocks until sarama takes the message, so a request deadline
// does not cut it short. A message handed over while Close/AsyncClose is under
// way comes back on Errors() with sarama.ErrShuttingDown, as raw sarama's
// does, and its span records the error. One handed over once the shutdown has
// closed sarama's input is dropped, its span recording the same error, where a
// send on Input() panics. A send racing that close is a data race, as it is on
// raw sarama's Input().
func (p *asyncProducer) InputContext(ctx context.Context, msg *sarama.ProducerMessage) {
	// A disabled agent traces nothing and injects nothing - not even the
	// unsampled marker - so the message headers are left untouched. A nested
	// message (isNested) is sent as it is for the same reason.
	tracer := pinpoint.FromContext(ctx)
	if !pinpoint.GetAgent().Enable() || isNested(tracer, msg) {
		sendAsyncProducerMessage(p.AsyncProducer.Input(), msg)
		return
	}

	// The hand-over is an event on the caller's own call stack, and the send
	// the broker acks is a goroutine span forked under it. The event is what a
	// goroutine tracer links to: forking straight off the caller's tracer
	// returned a noop tracer - nothing recorded, no headers written - whenever
	// the caller had no event open, which is every caller at span level (a
	// batch job, a hand-built NewSpanTracer), with only a throttled log to say
	// so. An HTTP handler worked by accident, under the handler's own event.
	tracer.NewSpanEvent("sarama.AsyncProducer.Input()")
	defer tracer.EndSpanEvent()

	// The span is saved before the send, so a broker ack cannot beat it.
	span, track := newAsyncProducerTracer(tracer.NewGoroutineTracer(), p.addrs, msg, p.config)
	saveAsyncProducerTracer(p, span, msg, track)
	if !sendAsyncProducerMessage(p.AsyncProducer.Input(), msg) {
		endAsyncProducerTracer(p, msg, sarama.ErrShuttingDown)
	}
}

func (p *asyncProducer) Successes() <-chan *sarama.ProducerMessage {
	return p.successes
}

func (p *asyncProducer) Errors() <-chan *sarama.ProducerError {
	return p.errors
}

// AsyncClose triggers the shutdown and returns without waiting for it, as
// sarama's own does. A repeated call does nothing: sarama's own sends its
// shutdown marker again and panics once the first has closed its input.
func (p *asyncProducer) AsyncClose() {
	p.closeOnce.Do(p.AsyncProducer.AsyncClose)
}

// Close mirrors sarama's own Close - AsyncClose, discard the remaining
// successes, collect the remaining errors - but drains through the wrapper's
// channels. Calling the underlying Close instead would have sarama's internal
// drain and forwardAcks compete for the same delivery events, so some spans
// would never be ended and the returned ProducerErrors would be incomplete.
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

// NewAsyncProducer wraps sarama.NewAsyncProducer and returns a AsyncProducer ready to instrument.
// It requires the underlying sarama Config.Producer.Return.Successes,
// so we can know whether successes will be returned.
func NewAsyncProducer(addrs []string, config *sarama.Config) (AsyncProducer, error) {
	producer, err := sarama.NewAsyncProducer(addrs, config)
	if err != nil {
		return nil, err
	}

	return WrapAsyncProducer(producer, addrs, config), nil
}

// WrapAsyncProducer wraps a sarama.AsyncProducer created elsewhere the way
// NewAsyncProducer wraps the one it creates. config is the producer's
// configuration (nil means sarama's default): Producer.Return.Successes and
// Errors decide whether delivery acks end the message spans. addrs are the
// broker addresses; the first is the span event's destination. A producer
// that is already wrapped is returned as it is, and a nil one - what
// sarama.NewAsyncProducer returns with its error - as nil: the wrapper's
// delivery goroutine read its channels and crashed the process. The
// compile-time instrumentation tool uses it from its sarama.NewAsyncProducer
// hook.
func WrapAsyncProducer(producer sarama.AsyncProducer, addrs []string, config *sarama.Config) AsyncProducer {
	if producer == nil {
		return nil
	}
	if p, ok := producer.(*asyncProducer); ok {
		return p
	}
	return wrapAsyncProducer(producer, addrs, config)
}

func wrapAsyncProducer(producer sarama.AsyncProducer, addrs []string, config *sarama.Config) *asyncProducer {
	if config == nil {
		// sarama.NewAsyncProducer accepts a nil config and substitutes
		// NewConfig() itself; the channel sizes below and every InputContext
		// read it.
		config = sarama.NewConfig()
	}

	// The ack channels are buffered by ChannelBufferSize: forwardAcks is one
	// goroutine between sarama and the application, and an unbuffered
	// hand-off made every ack a rendezvous with it.
	wrapped := &asyncProducer{
		AsyncProducer: producer,
		addrs:         addrs,
		config:        config,
		successes:     make(chan *sarama.ProducerMessage, config.ChannelBufferSize),
		errors:        make(chan *sarama.ProducerError, config.ChannelBufferSize),
		spans:         make(map[*sarama.ProducerMessage]pinpoint.Tracer),
	}

	go wrapped.forwardAcks()

	return wrapped
}

// forwardAcks moves the underlying producer's acks to the wrapper's
// Successes and Errors, ending each message's tracer on the way, until the
// producer has closed both.
func (p *asyncProducer) forwardAcks() {
	successes, errs := p.AsyncProducer.Successes(), p.AsyncProducer.Errors()
	for successes != nil || errs != nil {
		select {
		case msg, ok := <-successes:
			if !ok {
				successes = nil
				continue
			}
			endAsyncProducerTracer(p, msg, nil)
			p.successes <- msg
		case e, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			endAsyncProducerTracer(p, e.Msg, e.Err)
			p.errors <- e
		}
	}

	// The wrapper's pair closes only after sarama's: AsyncClose guarantees
	// delivery of every in-flight message on Successes/Errors before closing
	// them, and the user is entitled to drain all of it through the wrapper.
	// sarama closes its input before its acks, so a span InputContext saves
	// after this sweep belongs to a send that finds the input closed and ends
	// the span itself.
	endRemainingAsyncProducerTracers(p)
	close(p.errors)
	close(p.successes)
}

// sendAsyncProducerMessage sends msg on sarama's input and reports whether it
// went: a send once the shutdown has closed that input is refused instead of
// panicking the caller of InputContext.
func sendAsyncProducerMessage(input chan<- *sarama.ProducerMessage, msg *sarama.ProducerMessage) (sent bool) {
	defer func() {
		if recover() != nil {
			sent = false
		}
	}()
	input <- msg
	return true
}

// HeaderAsyncSpanId is the record header earlier releases wrote to match an
// ack to its span. It is no longer written - the span map is keyed by the
// message, which sarama returns as it is - and is kept for the header writer,
// which drops it from a message sent again.
const HeaderAsyncSpanId = "Pinpoint-AsyncSpanID"

// trackAcks reports whether delivery acks can be relied on to end the tracers.
// Successes alone is not enough: with Return.Errors off, a failed message gets
// no ack at all, and its tracer would sit in the span map until shutdown.
func trackAcks(config *sarama.Config) bool {
	return config.Producer.Return.Successes && config.Producer.Return.Errors
}

// newAsyncProducerTracer also reports whether the ack path will end the span,
// in which case the caller keeps it in the span map until the ack.
func newAsyncProducerTracer(tracer pinpoint.Tracer, addrs []string, msg *sarama.ProducerMessage, config *sarama.Config) (pinpoint.Tracer, bool) {
	tracer.NewSpanEvent("sarama.AsyncProducer.SendMessage")
	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeKafkaClient)
	se.Annotations().AppendString(pinpoint.AnnotationKafkaTopic, msg.Topic)
	// A wrapped producer (WrapAsyncProducer) may come without an address.
	if len(addrs) > 0 {
		se.SetDestination(addrs[0])
	}

	// Without headers no trace context travels with the message, so the span
	// is ended at save time instead of waiting for the ack.
	if !headersSupported(config) {
		return tracer, false
	}
	tracer.Inject(&distributedTracingContextWriterProducer{msg: msg})
	return tracer, trackAcks(config) && tracer.IsSampled()
}

func saveAsyncProducerTracer(wrapped *asyncProducer, span pinpoint.Tracer, msg *sarama.ProducerMessage, track bool) {
	if track {
		wrapped.spansLock.Lock()
		defer wrapped.spansLock.Unlock()

		wrapped.spans[msg] = span
	} else {
		span.EndSpanEvent()
		span.EndSpan()
	}
}

func endAsyncProducerTracer(wrapped *asyncProducer, msg *sarama.ProducerMessage, err error) {
	wrapped.spansLock.Lock()
	span, ok := wrapped.spans[msg]
	delete(wrapped.spans, msg)
	wrapped.spansLock.Unlock()

	if ok {
		if err != nil {
			span.SpanEvent().SetError(err)
		}
		span.EndSpanEvent()
		span.EndSpan()
	}
}

// endRemainingAsyncProducerTracers ends whatever is left once sarama has closed
// its delivery channels. Those messages never got an ack, so their spans record
// the shutdown rather than a send that looks like it succeeded.
func endRemainingAsyncProducerTracers(wrapped *asyncProducer) {
	wrapped.spansLock.Lock()
	spans := wrapped.spans
	wrapped.spans = make(map[*sarama.ProducerMessage]pinpoint.Tracer)
	wrapped.spansLock.Unlock()

	for _, span := range spans {
		span.SpanEvent().SetError(sarama.ErrShuttingDown)
		span.EndSpanEvent()
		span.EndSpan()
	}
}
