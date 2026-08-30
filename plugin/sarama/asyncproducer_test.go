package ppsarama

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Shopify/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

type stubAsyncProducer struct {
	sarama.AsyncProducer
	input     chan *sarama.ProducerMessage
	successes chan *sarama.ProducerMessage
	errors    chan *sarama.ProducerError
	inputSeen chan struct{}
	inputOnce sync.Once
	onClose   func()
}

func (s *stubAsyncProducer) AsyncClose() {
	if s.onClose != nil {
		s.onClose()
	}
}
func (s *stubAsyncProducer) Close() error { return nil }
func (s *stubAsyncProducer) Input() chan<- *sarama.ProducerMessage {
	s.inputOnce.Do(func() { close(s.inputSeen) })
	return s.input
}
func (s *stubAsyncProducer) Successes() <-chan *sarama.ProducerMessage { return s.successes }
func (s *stubAsyncProducer) Errors() <-chan *sarama.ProducerError      { return s.errors }

func newStubAsyncProducer() *stubAsyncProducer {
	return &stubAsyncProducer{
		input:     make(chan *sarama.ProducerMessage, 8),
		successes: make(chan *sarama.ProducerMessage, 8),
		errors:    make(chan *sarama.ProducerError, 8),
		inputSeen: make(chan struct{}),
	}
}

type recordingSpanEvent struct {
	pinpoint.SpanEventRecorder
	err error
}

func (e *recordingSpanEvent) SetError(err error, _ ...string) { e.err = err }

type recordingTracer struct {
	pinpoint.Tracer
	id      string
	se      *recordingSpanEvent
	ended   chan struct{}
	endOnce sync.Once
}

func newRecordingTracer(id string) *recordingTracer {
	noop := pinpoint.NoopTracer()
	return &recordingTracer{
		Tracer: noop,
		id:     id,
		se:     &recordingSpanEvent{SpanEventRecorder: noop.SpanEvent()},
		ended:  make(chan struct{}),
	}
}

func (t *recordingTracer) SpanEvent() pinpoint.SpanEventRecorder { return t.se }

func (t *recordingTracer) NewGoroutineTracer() pinpoint.Tracer { return t }
func (t *recordingTracer) NewSpanEvent(string) pinpoint.Tracer { return t }
func (t *recordingTracer) IsSampled() bool                     { return true }
func (t *recordingTracer) AsyncSpanId() string                 { return t.id }
func (t *recordingTracer) EndSpan() {
	t.endOnce.Do(func() { close(t.ended) })
}

func waitForClose(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

// requireSpanError must follow the tracer's ended signal, which is what orders
// the wrapper's write against this read.
func requireSpanError(t *testing.T, tracer *recordingTracer, want error) {
	t.Helper()
	if tracer.se.err != want {
		t.Fatalf("span event error = %v, want %v", tracer.se.err, want)
	}
}

func requireSpanCount(t *testing.T, p *asyncProducer, want int) {
	t.Helper()
	p.spansLock.Lock()
	got := len(p.spans)
	p.spansLock.Unlock()
	if got != want {
		t.Fatalf("stored tracer count = %d, want %d", got, want)
	}
}

// Acknowledgments still in flight when AsyncClose is called must all reach the
// user, exactly as raw sarama guarantees, and the wrapper's channels must close
// afterwards.
func Test_asyncProducer_AsyncCloseDrainsInFlightAcks(t *testing.T) {
	config := sarama.NewConfig()
	config.Producer.Return.Successes = true

	stub := newStubAsyncProducer()
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, config)

	sent := make([]*sarama.ProducerMessage, 3)
	tracers := make([]*recordingTracer, len(sent))
	for i := range sent {
		sent[i] = &sarama.ProducerMessage{Topic: "topic"}
		tracers[i] = newRecordingTracer(string(rune('a' + i)))
		ctx := pinpoint.NewContext(context.Background(), tracers[i])
		p.InputContext(ctx, sent[i])
	}
	for range sent {
		<-stub.input
	}

	p.AsyncClose()

	// The broker acks arrive only after the close.
	for _, msg := range sent {
		stub.successes <- msg
	}
	close(stub.successes)
	close(stub.errors)

	got := 0
	for range p.Successes() {
		got++
	}
	if got != len(sent) {
		t.Errorf("drained %d successes after AsyncClose, want %d", got, len(sent))
	}
	if _, ok := <-p.Errors(); ok {
		t.Errorf("Errors channel not closed after shutdown")
	}
	for _, tracer := range tracers {
		waitForClose(t, tracer.ended, "acknowledged tracer")
		requireSpanError(t, tracer, nil)
	}
	requireSpanCount(t, p, 0)
}

// The Input path - WithContext plus the raw channel - must save its tracer and
// end it on the broker's verdict, exactly as InputContext's does. Both verdicts
// are covered: a delivery error has to reach the span, not just the shutdown
// errors the close tests record.
func Test_asyncProducer_InputAckEndsTracer(t *testing.T) {
	tests := []struct {
		name string
		ack  func(*stubAsyncProducer, *sarama.ProducerMessage)
		recv func(*testing.T, *asyncProducer, *sarama.ProducerMessage)
		want error
	}{
		{
			name: "success",
			ack: func(stub *stubAsyncProducer, msg *sarama.ProducerMessage) {
				stub.successes <- msg
			},
			recv: func(t *testing.T, p *asyncProducer, msg *sarama.ProducerMessage) {
				t.Helper()
				if got := <-p.Successes(); got != msg {
					t.Fatalf("Successes delivered %v, want %v", got, msg)
				}
			},
		},
		{
			name: "error",
			ack: func(stub *stubAsyncProducer, msg *sarama.ProducerMessage) {
				stub.errors <- &sarama.ProducerError{Msg: msg, Err: sarama.ErrOutOfBrokers}
			},
			recv: func(t *testing.T, p *asyncProducer, msg *sarama.ProducerMessage) {
				t.Helper()
				got := <-p.Errors()
				if got.Msg != msg || !errors.Is(got.Err, sarama.ErrOutOfBrokers) {
					t.Fatalf("Errors delivered %v, want %v on %v", got, sarama.ErrOutOfBrokers, msg)
				}
			},
			want: sarama.ErrOutOfBrokers,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := sarama.NewConfig()
			config.Producer.Return.Successes = true

			stub := newStubAsyncProducer()
			stub.onClose = func() {
				close(stub.successes)
				close(stub.errors)
			}
			p := wrapAsyncProducer(stub, []string{"broker:9092"}, config)

			tracer := newRecordingTracer(tt.name)
			p.WithContext(pinpoint.NewContext(context.Background(), tracer))
			msg := &sarama.ProducerMessage{Topic: "topic"}
			p.Input() <- msg
			<-stub.input
			requireSpanCount(t, p, 1)

			// The tracer is ended before the ack is handed on, so receiving it
			// orders the assertions below.
			tt.ack(stub, msg)
			tt.recv(t, p, msg)
			waitForClose(t, tracer.ended, "acknowledged tracer")
			requireSpanError(t, tracer, tt.want)
			requireSpanCount(t, p, 0)

			p.AsyncClose()
			for range p.Successes() {
			}
			for range p.Errors() {
			}
			waitForClose(t, p.drainDone, "input drainer")
		})
	}
}

func Test_asyncProducer_AsyncCloseCancelsBlockedInput(t *testing.T) {
	tests := []struct {
		name string
		send func(*asyncProducer, context.Context, *sarama.ProducerMessage)
	}{
		{
			name: "Input",
			send: func(p *asyncProducer, ctx context.Context, msg *sarama.ProducerMessage) {
				p.WithContext(ctx)
				p.Input() <- msg
			},
		},
		{
			name: "InputContext",
			send: func(p *asyncProducer, ctx context.Context, msg *sarama.ProducerMessage) {
				p.InputContext(ctx, msg)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := sarama.NewConfig()
			config.Producer.Return.Successes = true

			stub := newStubAsyncProducer()
			stub.input = make(chan *sarama.ProducerMessage)
			stub.onClose = func() {
				close(stub.successes)
				close(stub.errors)
			}
			p := wrapAsyncProducer(stub, []string{"broker:9092"}, config)
			tracer := newRecordingTracer(tt.name)
			ctx := pinpoint.NewContext(context.Background(), tracer)

			inputReturned := make(chan struct{})
			go func() {
				tt.send(p, ctx, &sarama.ProducerMessage{Topic: "topic"})
				close(inputReturned)
			}()
			waitForClose(t, stub.inputSeen, "blocked underlying input")
			waitForClose(t, inputReturned, "wrapper input")
			requireSpanCount(t, p, 1)

			closeReturned := make(chan struct{})
			go func() {
				p.AsyncClose()
				close(closeReturned)
			}()
			waitForClose(t, closeReturned, "AsyncClose")
			waitForClose(t, tracer.ended, "cancelled tracer")
			requireSpanError(t, tracer, sarama.ErrShuttingDown)
			requireSpanCount(t, p, 0)

			if _, ok := <-p.Successes(); ok {
				t.Error("Successes channel not closed after shutdown")
			}
			if _, ok := <-p.Errors(); ok {
				t.Error("Errors channel not closed after shutdown")
			}
		})
	}
}

func Test_asyncProducer_InputContextAfterAsyncCloseReturns(t *testing.T) {
	stub := newStubAsyncProducer()
	stub.onClose = func() {
		close(stub.successes)
		close(stub.errors)
	}
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, sarama.NewConfig())
	p.AsyncClose()

	returned := make(chan struct{})
	go func() {
		p.InputContext(context.Background(), &sarama.ProducerMessage{Topic: "topic"})
		close(returned)
	}()
	waitForClose(t, returned, "InputContext after AsyncClose")
	if _, ok := <-p.Successes(); ok {
		t.Error("Successes channel not closed after shutdown")
	}
	if _, ok := <-p.Errors(); ok {
		t.Error("Errors channel not closed after shutdown")
	}
}

// A send that begins while AsyncClose is still shutting down must return
// rather than park forever: AsyncClose releases as soon as the input forwarder
// is gone, so the drainer has to keep receiving until the wrapper is closed.
// AsyncClose must return without waiting for the shutdown it triggered, the way
// raw sarama's does, even though the underlying close has to wait for the input
// forwarder first.
func Test_asyncProducer_AsyncCloseDoesNotBlock(t *testing.T) {
	stub := newStubAsyncProducer()
	release := make(chan struct{})
	stub.onClose = func() {
		<-release
		close(stub.successes)
		close(stub.errors)
	}
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, sarama.NewConfig())

	returned := make(chan struct{})
	go func() {
		p.AsyncClose()
		close(returned)
	}()
	waitForClose(t, returned, "AsyncClose")
	p.AsyncClose() // a repeat call must not block on the pending shutdown either

	close(release)
	if _, ok := <-p.Successes(); ok {
		t.Error("Successes channel not closed after shutdown")
	}
	if _, ok := <-p.Errors(); ok {
		t.Error("Errors channel not closed after shutdown")
	}
	waitForClose(t, p.drainDone, "input drainer")
}

func Test_asyncProducer_InputDuringAsyncCloseReturns(t *testing.T) {
	stub := newStubAsyncProducer()
	closing, release := make(chan struct{}), make(chan struct{})
	stub.onClose = func() {
		close(closing)
		<-release
		close(stub.successes)
		close(stub.errors)
	}
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, sarama.NewConfig())

	closeReturned := make(chan struct{})
	go func() {
		p.AsyncClose()
		close(closeReturned)
	}()
	// The input forwarder is gone and the underlying shutdown is pending, so
	// only the drainer can still receive from the wrapper's input.
	waitForClose(t, closing, "underlying AsyncClose")

	sent := make(chan struct{})
	go func() {
		p.Input() <- &sarama.ProducerMessage{Topic: "topic"}
		close(sent)
	}()
	waitForClose(t, sent, "Input racing AsyncClose")

	close(release)
	waitForClose(t, closeReturned, "AsyncClose")
	if _, ok := <-p.Successes(); ok {
		t.Error("Successes channel not closed after shutdown")
	}
	if _, ok := <-p.Errors(); ok {
		t.Error("Errors channel not closed after shutdown")
	}
	waitForClose(t, p.drainDone, "input drainer")
}

// Once shutdown has completed there is no receiver left, so a send on Input is
// a programming error. It must fail loudly like raw sarama's closed input
// rather than park forever.
func Test_asyncProducer_InputAfterShutdownPanics(t *testing.T) {
	stub := newStubAsyncProducer()
	stub.onClose = func() {
		close(stub.successes)
		close(stub.errors)
	}
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, sarama.NewConfig())
	if err := p.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
	waitForClose(t, p.drainDone, "input drainer")

	panicked := make(chan any, 1)
	go func() {
		defer func() { panicked <- recover() }()
		p.Input() <- &sarama.ProducerMessage{Topic: "topic"}
	}()
	select {
	case v := <-panicked:
		if v == nil {
			t.Fatal("Input after shutdown did not panic")
		}
	case <-time.After(time.Second):
		t.Fatal("Input after shutdown blocked instead of panicking")
	}
}

func Test_asyncProducer_UnderlyingInputPanicCleansTracer(t *testing.T) {
	config := sarama.NewConfig()
	config.Producer.Return.Successes = true

	stub := newStubAsyncProducer()
	close(stub.input)
	stub.onClose = func() {
		close(stub.successes)
		close(stub.errors)
	}
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, config)
	tracer := newRecordingTracer("panic")
	ctx := pinpoint.NewContext(context.Background(), tracer)

	inputReturned := make(chan struct{})
	go func() {
		p.InputContext(ctx, &sarama.ProducerMessage{Topic: "topic"})
		close(inputReturned)
	}()
	waitForClose(t, inputReturned, "wrapper input")
	waitForClose(t, p.inputDone, "input forwarder")
	waitForClose(t, tracer.ended, "panicked-send tracer")
	requireSpanError(t, tracer, sarama.ErrShuttingDown)
	requireSpanCount(t, p, 0)

	p.AsyncClose()
	if _, ok := <-p.Successes(); ok {
		t.Error("Successes channel not closed after shutdown")
	}
	if _, ok := <-p.Errors(); ok {
		t.Error("Errors channel not closed after shutdown")
	}
}

func Test_asyncProducer_ShutdownEndsRemainingTracer(t *testing.T) {
	config := sarama.NewConfig()
	config.Producer.Return.Successes = true

	stub := newStubAsyncProducer()
	stub.onClose = func() {
		close(stub.successes)
		close(stub.errors)
	}
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, config)
	tracer := newRecordingTracer("remaining")
	ctx := pinpoint.NewContext(context.Background(), tracer)

	p.InputContext(ctx, &sarama.ProducerMessage{Topic: "topic"})
	<-stub.input
	requireSpanCount(t, p, 1)
	p.AsyncClose()

	if _, ok := <-p.Successes(); ok {
		t.Error("Successes channel not closed after shutdown")
	}
	if _, ok := <-p.Errors(); ok {
		t.Error("Errors channel not closed after shutdown")
	}
	waitForClose(t, tracer.ended, "remaining tracer")
	requireSpanError(t, tracer, sarama.ErrShuttingDown)
	requireSpanCount(t, p, 0)
	waitForClose(t, p.drainDone, "input drainer")
}

// Close must return the undelivered messages as ProducerErrors, like raw
// sarama's Close, with every event drained through the wrapper.
func Test_asyncProducer_CloseCollectsErrors(t *testing.T) {
	config := sarama.NewConfig()

	stub := newStubAsyncProducer()
	stub.errors <- &sarama.ProducerError{
		Msg: &sarama.ProducerMessage{Topic: "topic"},
		Err: sarama.ErrOutOfBrokers,
	}
	close(stub.successes)
	close(stub.errors)

	p := wrapAsyncProducer(stub, []string{"broker:9092"}, config)

	err := p.Close()
	var perrs sarama.ProducerErrors
	if !errors.As(err, &perrs) {
		t.Fatalf("Close() = %v, want sarama.ProducerErrors", err)
	}
	if len(perrs) != 1 {
		t.Errorf("Close() collected %d errors, want 1", len(perrs))
	}
}
