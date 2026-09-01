package ppsaramaibm

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		require.FailNow(t, "timed out waiting for "+name)
	}
}

// requireChannelClosed reads the channel dry and fails if it is still open:
// after shutdown the wrapper owes the caller a closed channel, not a stall.
func requireChannelsClosed(t *testing.T, p *asyncProducer) {
	t.Helper()
	_, ok := <-p.Successes()
	assert.False(t, ok, "Successes channel not closed after shutdown")
	_, ok = <-p.Errors()
	assert.False(t, ok, "Errors channel not closed after shutdown")
}

// requireSpanError must follow the tracer's ended signal, which is what orders
// the wrapper's write against this read.
func requireSpanError(t *testing.T, tracer *recordingTracer, want error) {
	t.Helper()
	require.Equal(t, want, tracer.se.err, "the span event recorded the wrong verdict")
}

func requireSpanCount(t *testing.T, p *asyncProducer, want int) {
	t.Helper()
	p.spansLock.Lock()
	got := len(p.spans)
	p.spansLock.Unlock()
	require.Equal(t, want, got, "the wrapper is holding the wrong number of tracers")
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
	assert.Equal(t, len(sent), got, "every in-flight ack must still reach the caller after AsyncClose")
	_, ok := <-p.Errors()
	assert.False(t, ok, "Errors channel not closed after shutdown")
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
				require.Same(t, msg, <-p.Successes(), "Successes delivered a different message")
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
				require.Same(t, msg, got.Msg, "Errors delivered a different message")
				require.ErrorIs(t, got.Err, sarama.ErrOutOfBrokers)
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

			requireChannelsClosed(t, p)
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
	requireChannelsClosed(t, p)
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
	requireChannelsClosed(t, p)
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
	requireChannelsClosed(t, p)
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
	require.NoError(t, p.Close())
	waitForClose(t, p.drainDone, "input drainer")

	panicked := make(chan any, 1)
	go func() {
		defer func() { panicked <- recover() }()
		p.Input() <- &sarama.ProducerMessage{Topic: "topic"}
	}()
	select {
	case v := <-panicked:
		require.NotNil(t, v, "sending on Input after shutdown did not panic")
	case <-time.After(time.Second):
		require.FailNow(t, "Input after shutdown blocked instead of panicking")
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
	requireChannelsClosed(t, p)
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

	requireChannelsClosed(t, p)
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
	require.ErrorAs(t, err, &perrs, "Close must report undelivered messages as sarama.ProducerErrors")
	require.Len(t, perrs, 1)
	assert.ErrorIs(t, perrs[0].Err, sarama.ErrOutOfBrokers)
}

// A nil sarama.Config is legal for sarama.NewAsyncProducer, so the wrapper must
// not let it kill the input forwarder - every later message would be consumed
// and silently dropped by the drainer.
func Test_asyncProducer_NilConfigStillDeliversMessages(t *testing.T) {
	stub := newStubAsyncProducer()
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, nil)

	tracer := newRecordingTracer("a")
	msg := &sarama.ProducerMessage{Topic: "topic"}
	p.InputContext(pinpoint.NewContext(context.Background(), tracer), msg)

	select {
	case got := <-stub.input:
		require.Same(t, msg, got, "the wrong message reached the underlying producer")
	case <-time.After(time.Second):
		require.FailNow(t, "message never reached the underlying producer")
	}

	// nil config means Return.Successes=false: the span must be ended
	// immediately instead of waiting for an ack that will never come.
	waitForClose(t, tracer.ended, "span end")
	requireSpanCount(t, p, 0)
}

// The standard retry pattern re-sends the very message object taken off
// Errors(). The second send must replace the pinpoint headers, not append a
// second set: Get returns the first match, so a stale appended id would make
// the retry's ack miss the span map and leak its tracer until shutdown.
func Test_asyncProducer_RetriedMessageReplacesHeaders(t *testing.T) {
	config := sarama.NewConfig()
	config.Producer.Return.Successes = true

	stub := newStubAsyncProducer()
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, config)

	msg := &sarama.ProducerMessage{Topic: "topic"}

	first := newRecordingTracer("id-1")
	p.InputContext(pinpoint.NewContext(context.Background(), first), msg)
	<-stub.input
	stub.errors <- &sarama.ProducerError{Msg: msg, Err: sarama.ErrOutOfBrokers}
	<-p.Errors()
	waitForClose(t, first.ended, "first attempt's span end")

	retry := newRecordingTracer("id-2")
	p.InputContext(pinpoint.NewContext(context.Background(), retry), msg)
	<-stub.input

	ids := 0
	for _, h := range msg.Headers {
		if string(h.Key) == HeaderAsyncSpanId {
			ids++
			assert.Equal(t, "id-2", string(h.Value), "the header must carry the retry's id")
		}
	}
	require.Equal(t, 1, ids, "the retry appended a second async span id header")

	stub.successes <- msg
	<-p.Successes()
	waitForClose(t, retry.ended, "retry's span end")
	requireSpanCount(t, p, 0)
}
