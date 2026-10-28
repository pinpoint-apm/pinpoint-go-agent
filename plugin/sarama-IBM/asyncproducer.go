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

type asyncProducer struct {
	sarama.AsyncProducer
	addrs  []string
	config *sarama.Config
	// input carries what Input and InputContext both accept, in one FIFO: two
	// channels the forwarder picked between at random reordered one
	// goroutine's messages across the two, and a key's messages with them.
	input chan *sarama.ProducerMessage
	// inputLock is held shared by InputContext's sends and exclusively by the
	// drainer while it closes input, which sets inputClosed.
	inputLock   sync.RWMutex
	inputClosed bool
	// refused hands forwardAcks the messages sent once the forwarder had gone,
	// to return on Errors() as sarama's dispatcher does while it shuts down.
	refused   chan *sarama.ProducerMessage
	successes chan *sarama.ProducerMessage
	errors    chan *sarama.ProducerError
	done      chan struct{}
	inputDone chan struct{}
	ackDone   chan struct{}
	drainDone chan struct{}
	flush     chan chan struct{}
	closeOnce sync.Once
	// spans holds the tracer of every message waiting for its ack, under the
	// message itself: sarama hands the same *ProducerMessage back on
	// Successes and Errors, so the ack needs no id. One message object in
	// flight twice at once keeps only the later span; the retry pattern that
	// re-sends an object does so after its ack, and is nested anyway.
	spans     map[*sarama.ProducerMessage]pinpoint.Tracer
	spansLock sync.Mutex
}

// CommitTxn and AbortTxn first hand sarama every message the wrapper has
// accepted. sarama ends a transaction by queuing a marker on its own input
// behind the messages sent before it, and a message still in the wrapper's
// buffer reached sarama after the marker: rejected as outside a transaction
// while the commit reported success, or racing the commit's wait into a
// panic.
func (p *asyncProducer) CommitTxn() error {
	p.flushInput()
	return p.AsyncProducer.CommitTxn()
}

func (p *asyncProducer) AbortTxn() error {
	p.flushInput()
	return p.AsyncProducer.AbortTxn()
}

// forwardBuffered hands sarama what input holds now, up to its length, which
// covers every message accepted before the call and ends even while other
// goroutines keep sending. The forwarder is the only receiver on input, so the
// receives cannot block. False once sarama is gone.
func (p *asyncProducer) forwardBuffered() bool {
	for n := len(p.input); n > 0; n-- {
		if !p.forward(<-p.input) {
			return false
		}
	}
	return true
}

// flushInput returns once the forwarder has handed sarama every message the
// wrapper accepted before the call, or has gone.
func (p *asyncProducer) flushInput() {
	flushed := make(chan struct{})
	select {
	case p.flush <- flushed:
	case <-p.inputDone:
		return
	}
	select {
	case <-flushed:
	case <-p.inputDone:
	}
}

// InputContext sends a given message with tracer context to the input channel of sarama.AsyncProducer.
//
// It sends on the channel Input() returns, so one goroutine's messages keep
// their order across the two. The send does not watch ctx.Done(): like
// Input(), it blocks until the wrapper takes the message, so a request
// deadline does not cut it short. A message handed over while Close/AsyncClose
// is under way comes back on Errors() with sarama.ErrShuttingDown, as raw
// sarama's does, and its span records the error. One handed over as the
// shutdown completes, or after, is dropped, where raw sarama panics on its
// closed channels.
func (p *asyncProducer) InputContext(ctx context.Context, msg *sarama.ProducerMessage) {
	// Held shared until the message is sent or turned away: the drainer closes
	// input under it exclusively, so the send never races that close, and the
	// drainer sweeps up whatever was sent, ending its span, before closing.
	p.inputLock.RLock()
	defer p.inputLock.RUnlock()
	if p.inputClosed {
		return
	}

	// A disabled agent traces nothing and injects nothing - not even the
	// unsampled marker - so the message headers are left untouched. A nested
	// message (isNested) is sent as it is for the same reason.
	if !pinpoint.GetAgent().Enable() || isNested(msg) {
		p.send(msg)
		return
	}

	// The hand-over is an event on the caller's own call stack, and the send
	// the broker acks is a goroutine span forked under it. The event is what a
	// goroutine tracer links to: forking straight off the caller's tracer
	// returned a noop tracer - nothing recorded, no headers written - whenever
	// the caller had no event open, which is every caller at span level (a
	// batch job, a hand-built NewSpanTracer), with only a throttled log to say
	// so. An HTTP handler worked by accident, under the handler's own event.
	tracer := pinpoint.FromContext(ctx)
	tracer.NewSpanEvent("sarama.AsyncProducer.Input()")
	defer tracer.EndSpanEvent()

	// The span is created and saved here, in the caller's goroutine: producer
	// goroutines are not serialized behind the input forwarder, and the save
	// still strictly precedes the send, so a broker ack cannot beat it.
	span, track := newAsyncProducerTracer(tracer.NewGoroutineTracer(), p.addrs, msg, p.config)
	saveAsyncProducerTracer(p, span, msg, track)
	if !p.send(msg) {
		endAsyncProducerTracer(p, msg, sarama.ErrShuttingDown)
	}
}

// send hands InputContext's message to input, or reports that the wrapper has
// shut down: ackDone closes before the drainer takes inputLock to close input,
// so a send blocked on a full input lets the drainer go ahead.
func (p *asyncProducer) send(msg *sarama.ProducerMessage) bool {
	select {
	case p.input <- msg:
		return true
	case <-p.ackDone:
		return false
	}
}

// Input returns the input channel; a message sent on it is produced without
// tracing. Use InputContext, which sends on the same channel. A message sent
// while Close/AsyncClose is under way comes back on Errors() with
// sarama.ErrShuttingDown, as raw sarama's does.
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
		// NewConfig() itself. Without the same substitution here, the
		// forwarder's first config read panics, the recover below kills the
		// forwarder, and every later message is drained and silently dropped.
		config = sarama.NewConfig()
	}

	// Buffered like sarama's own channels: an unbuffered wrapper channel made
	// every message a rendezvous with a single goroutine and capped producer
	// throughput regardless of how many goroutines the application runs.
	wrapped := &asyncProducer{
		AsyncProducer: producer,
		addrs:         addrs,
		config:        config,
		input:         make(chan *sarama.ProducerMessage, config.ChannelBufferSize),
		refused:       make(chan *sarama.ProducerMessage),
		successes:     make(chan *sarama.ProducerMessage, config.ChannelBufferSize),
		errors:        make(chan *sarama.ProducerError, config.ChannelBufferSize),
		done:          make(chan struct{}),
		inputDone:     make(chan struct{}),
		ackDone:       make(chan struct{}),
		drainDone:     make(chan struct{}),
		flush:         make(chan chan struct{}),
		spans:         make(map[*sarama.ProducerMessage]pinpoint.Tracer),
	}

	go wrapped.forwardInput()
	go wrapped.forwardAcks()

	return wrapped
}

// forwardInput moves the messages the wrapper accepted on its inputs to the
// underlying producer, serving flushes in between, until the producer is
// closed; its comments explain the ordering.
func (p *asyncProducer) forwardInput() {
	// Keep a closed underlying input from panicking the host application.
	// AsyncClose waits for this goroutine before closing that input itself.
	// Nothing receives from the wrapper's input once this returns, so hand
	// it to a drainer before releasing AsyncClose.
	defer func() {
		go drainAsyncProducerInput(p)
		close(p.inputDone)
	}()
	defer func() {
		// A dead forwarder means every later message is refused by the
		// drainer, so its death must never be silent itself.
		if r := recover(); r != nil {
			pinpoint.Log("sarama").Errorf("async producer input forwarder died: %v", r)
		}
	}()

	for {
		select {
		case msg := <-p.input:
			if !p.forward(msg) {
				return
			}
		case flushed := <-p.flush:
			if !p.forwardBuffered() {
				return
			}
			close(flushed)
		case <-p.done:
			// What input holds now was accepted before the shutdown and goes
			// to sarama, which keeps consuming its own input until AsyncClose -
			// and AsyncClose waits for this goroutine. A message sent later is
			// the drainer's, which returns it on Errors() as sarama does once
			// it is shutting down: forwarding until input ran dry let a
			// steadily producing application stretch the shutdown without end.
			p.forwardBuffered()
			return
		}
	}
}

// forwardAcks moves the underlying producer's acks to the wrapper's
// Successes and Errors, ending each message's tracer on the way, until the
// producer has closed both.
func (p *asyncProducer) forwardAcks() {
	// Closed only here, and only after sarama has closed its own pair:
	// AsyncClose guarantees delivery of every in-flight message on
	// Successes/Errors before closing them, and the user is entitled to
	// drain all of it through the wrapper. p.input is closed by the
	// drainer below, once ackDone has turned InputContext's sends away.
	defer close(p.ackDone)
	defer close(p.successes)
	defer close(p.errors)
	defer endRemainingAsyncProducerTracers(p)

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
		case msg := <-p.refused:
			p.errors <- &sarama.ProducerError{Msg: msg, Err: sarama.ErrShuttingDown}
		}
	}
}

// forward hands a message to sarama's own input. A closed underlying input
// ends the message span here, and false says the forwarder is done.
func (p *asyncProducer) forward(msg *sarama.ProducerMessage) bool {
	if !sendAsyncProducerMessage(p.AsyncProducer.Input(), msg) {
		endAsyncProducerTracer(p, msg, sarama.ErrShuttingDown)
		return false
	}
	return true
}

func sendAsyncProducerMessage(input chan<- *sarama.ProducerMessage, msg *sarama.ProducerMessage) (sent bool) {
	defer func() {
		if recover() != nil {
			sent = false
		}
	}()
	// This message was accepted before shutdown. AsyncClose waits for the
	// forwarder before closing sarama's input, so backpressure must be allowed
	// to clear even after the wrapper stops accepting new messages.
	input <- msg
	return true
}

// drainAsyncProducerInput refuses what reaches input after the forwarder has
// gone - a message sent once the shutdown was under way, or one left behind
// when sarama's input was closed unexpectedly. It outlives AsyncClose - which
// returns as soon as the input forwarder is gone - and stops once the wrapper
// is fully shut down, sweeping up whoever parked in the meantime.
//
// It then closes input, so that a send on Input() issued after shutdown panics
// the caller exactly as raw sarama's own closed input does, instead of parking
// on a channel that nothing will ever receive from again. Racing such a send
// against that close is reported by the race detector, again just as it is
// for raw sarama - the send is a programming error either way. InputContext
// holds inputLock across its send, so the close waits for it, and it gives up
// on ackDone, which closes first.
func drainAsyncProducerInput(wrapped *asyncProducer) {
	defer close(wrapped.drainDone)

	for {
		select {
		case msg := <-wrapped.input:
			wrapped.refuse(msg)
		case <-wrapped.ackDone:
			wrapped.inputLock.Lock()
			wrapped.inputClosed = true
			for sweepAsyncProducerInput(wrapped) {
			}
			close(wrapped.input)
			wrapped.inputLock.Unlock()
			return
		}
	}
}

// refuse ends the span of a message the forwarder can no longer take and
// returns the message on Errors() with sarama.ErrShuttingDown, as sarama's
// dispatcher does with what reaches it while it shuts down: discarded, it was
// lost without the signal an application spools or retries from. forwardAcks
// owns Errors() and closes it, so the message goes through it; once forwardAcks
// has finished - a message caught in that last instant - it is only dropped,
// where sarama's own dispatcher panics on its closed Errors channel.
func (p *asyncProducer) refuse(msg *sarama.ProducerMessage) {
	endAsyncProducerTracer(p, msg, sarama.ErrShuttingDown)
	if !p.config.Producer.Return.Errors {
		return
	}
	select {
	case p.refused <- msg:
	case <-p.ackDone:
	}
}

// sweepAsyncProducerInput drops one parked message, ending its span, and
// reports whether it found one.
func sweepAsyncProducerInput(wrapped *asyncProducer) bool {
	select {
	case msg := <-wrapped.input:
		endAsyncProducerTracer(wrapped, msg, sarama.ErrShuttingDown)
		return true
	default:
		return false
	}
}

// HeaderAsyncSpanId is the record header earlier releases wrote to match an
// ack to its span. It is no longer written - the span map is keyed by the
// message, which sarama returns as it is - and is kept for isNested, which
// still reads a message carrying it as injected.
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
	tracer.Inject(newProducerHeaderWriter(msg))
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
