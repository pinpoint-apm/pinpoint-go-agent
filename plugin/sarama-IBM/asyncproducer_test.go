package ppsaramaibm

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
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

// newStubAsyncProducer closes its ack channels on AsyncClose, as sarama does;
// a test that acks after the close overrides onClose.
func newStubAsyncProducer() *stubAsyncProducer {
	s := &stubAsyncProducer{
		input:     make(chan *sarama.ProducerMessage, 8),
		successes: make(chan *sarama.ProducerMessage, 8),
		errors:    make(chan *sarama.ProducerError, 8),
		inputSeen: make(chan struct{}),
	}
	s.onClose = func() { close(s.successes); close(s.errors) }
	return s
}

// newConfig is sarama's default configuration at the first Kafka version with
// record headers: older sarama releases default to 0.8.2, below which the
// wrapper writes no trace header at all.
func newConfig() *sarama.Config {
	config := sarama.NewConfig()
	config.Version = sarama.V0_11_0_0
	return config
}

// ackConfig is newConfig with the success acks that end the async spans.
func ackConfig() *sarama.Config { c := newConfig(); c.Producer.Return.Successes = true; return c }

// txnStubAsyncProducer records how many messages had reached sarama's input
// when a transaction was ended.
type txnStubAsyncProducer struct {
	*stubAsyncProducer
	committedWith, abortedWith int
}

func (s *txnStubAsyncProducer) CommitTxn() error { s.committedWith = len(s.input); return nil }
func (s *txnStubAsyncProducer) AbortTxn() error  { s.abortedWith = len(s.input); return nil }

// sarama ends a transaction behind the messages already on its own input, so
// CommitTxn and AbortTxn must first hand it every message the wrapper accepted:
// one still in the wrapper's buffer landed after the marker.
func Test_asyncProducer_EndTxnForwardsAcceptedMessagesFirst(t *testing.T) {
	stub := &txnStubAsyncProducer{stubAsyncProducer: newStubAsyncProducer()}
	p := wrapAsyncProducer(stub, nil, newConfig())

	for i := 0; i < 4; i++ {
		p.Input() <- &sarama.ProducerMessage{Topic: "widgets"}
	}
	require.NoError(t, p.CommitTxn())
	assert.Equal(t, 4, stub.committedWith, "committed before the accepted messages reached sarama")

	for i := 0; i < 4; i++ {
		p.InputContext(context.Background(), &sarama.ProducerMessage{Topic: "widgets"})
	}
	require.NoError(t, p.AbortTxn())
	assert.Equal(t, 8, stub.abortedWith, "aborted before the accepted messages reached sarama")

	p.AsyncClose()
	requireChannelsClosed(t, p)
}

// Below Kafka 0.11 sarama rejects every message carrying headers, so the
// wrapper must leave a message's nil headers nil whenever it injects nothing -
// no tracer in the context - and inject nothing on such a producer, even for a
// sampled tracer, where the span must not then wait for an ack it cannot be
// matched to.
func TestProducers_WriteNoHeadersUnlessInjecting(t *testing.T) {
	startAgent(t)
	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/produce")
	defer tracer.EndSpan()
	// The async producer traces on a goroutine tracer, which needs an open
	// event to link to.
	defer tracer.NewSpanEvent("produce").EndSpanEvent()
	kafka010 := sarama.NewConfig()
	kafka010.Version = sarama.V0_10_2_0

	for _, tt := range []struct {
		name   string
		ctx    context.Context
		config *sarama.Config
	}{
		{"no tracer", context.Background(), newConfig()},
		{"before Kafka 0.11", pinpoint.NewContext(context.Background(), tracer), kafka010},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msg := &sarama.ProducerMessage{Topic: "widgets"}
			_, _, err := WrapSyncProducer(&stubSyncProducer{}, nil, tt.config).SendMessageContext(tt.ctx, msg)
			require.NoError(t, err)
			assert.Nil(t, msg.Headers, "sync producer")

			tt.config.Producer.Return.Successes = true // acks end the spans
			stub := newStubAsyncProducer()
			p := wrapAsyncProducer(stub, nil, tt.config)
			p.InputContext(tt.ctx, &sarama.ProducerMessage{Topic: "widgets"})
			assert.Nil(t, (<-stub.input).Headers, "async producer")
			requireSpanCount(t, p, 0)
			p.AsyncClose()
			requireChannelsClosed(t, p)
		})
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

// recv receives from ch or fails the test after a second.
func recv[T any](t *testing.T, ch <-chan T, name string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(time.Second):
		require.FailNow(t, "timed out waiting for "+name)
		panic("unreachable")
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
	startAgent(t)
	config := ackConfig()

	stub := newStubAsyncProducer()
	stub.onClose = nil
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
		recv(t, tracer.ended, "acknowledged tracer")
		require.Equal(t, nil, tracer.se.err, "the span event recorded the wrong verdict")
	}
	requireSpanCount(t, p, 0)
}

// The Input path - WithContext plus the raw channel - must save its tracer and
// end it on the broker's verdict, exactly as InputContext's does. Both verdicts
// are covered: a delivery error has to reach the span, not just the shutdown
// errors the close tests record.
func Test_asyncProducer_InputAckEndsTracer(t *testing.T) {
	startAgent(t)
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
			config := ackConfig()

			stub := newStubAsyncProducer()
			p := wrapAsyncProducer(stub, []string{"broker:9092"}, config)

			tracer := newRecordingTracer(tt.name)
			msg := &sarama.ProducerMessage{Topic: "topic"}
			p.InputContext(pinpoint.NewContext(context.Background(), tracer), msg)
			<-stub.input
			requireSpanCount(t, p, 1)

			// The tracer is ended before the ack is handed on, so receiving it
			// orders the assertions below.
			tt.ack(stub, msg)
			tt.recv(t, p, msg)
			recv(t, tracer.ended, "acknowledged tracer")
			require.Equal(t, tt.want, tracer.se.err, "the span event recorded the wrong verdict")
			requireSpanCount(t, p, 0)

			p.AsyncClose()
			for range p.Successes() {
			}
			for range p.Errors() {
			}
			recv(t, p.drainDone, "input drainer")
		})
	}
}

func Test_asyncProducer_AsyncCloseDeliversBlockedInput(t *testing.T) {
	startAgent(t)
	config := ackConfig()

	msg := &sarama.ProducerMessage{Topic: "topic"}
	stub := newStubAsyncProducer()
	stub.input = make(chan *sarama.ProducerMessage)
	stub.onClose = func() {
		stub.successes <- msg
		close(stub.successes)
		close(stub.errors)
	}
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, config)
	tracer := newRecordingTracer("InputContext")
	ctx := pinpoint.NewContext(context.Background(), tracer)

	inputReturned := make(chan struct{})
	go func() {
		p.InputContext(ctx, msg)
		close(inputReturned)
	}()
	recv(t, stub.inputSeen, "blocked underlying input")
	recv(t, inputReturned, "wrapper input")
	requireSpanCount(t, p, 1)

	closeReturned := make(chan struct{})
	go func() {
		p.AsyncClose()
		close(closeReturned)
	}()
	recv(t, closeReturned, "AsyncClose")
	require.Same(t, msg, recv(t, stub.input, "accepted message"))
	// The underlying shutdown hook publishes the delivery result
	// only after the forwarder has handed over the message.
	recv(t, tracer.ended, "acknowledged tracer")
	require.Equal(t, nil, tracer.se.err, "the span event recorded the wrong verdict")
	requireSpanCount(t, p, 0)

	require.Same(t, msg, <-p.Successes())
	requireChannelsClosed(t, p)
}

func Test_asyncProducer_InputContextAfterAsyncCloseReturns(t *testing.T) {
	stub := newStubAsyncProducer()
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, newConfig())
	p.AsyncClose()

	returned := make(chan struct{})
	go func() {
		p.InputContext(context.Background(), &sarama.ProducerMessage{Topic: "topic"})
		close(returned)
	}()
	recv(t, returned, "InputContext after AsyncClose")
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
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, newConfig())

	returned := make(chan struct{})
	go func() {
		p.AsyncClose()
		close(returned)
	}()
	recv(t, returned, "AsyncClose")
	p.AsyncClose() // a repeat call must not block on the pending shutdown either

	close(release)
	requireChannelsClosed(t, p)
	recv(t, p.drainDone, "input drainer")
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
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, newConfig())

	closeReturned := make(chan struct{})
	go func() {
		p.AsyncClose()
		close(closeReturned)
	}()
	// The input forwarder is gone and the underlying shutdown is pending, so
	// only the drainer can still receive from the wrapper's input.
	recv(t, closing, "underlying AsyncClose")

	sent := make(chan struct{})
	go func() {
		p.Input() <- &sarama.ProducerMessage{Topic: "topic"}
		close(sent)
	}()
	recv(t, sent, "Input racing AsyncClose")

	close(release)
	recv(t, closeReturned, "AsyncClose")
	requireChannelsClosed(t, p)
	recv(t, p.drainDone, "input drainer")
}

// Once shutdown has completed there is no receiver left, so a send on Input is
// a programming error. It must fail loudly like raw sarama's closed input
// rather than park forever.
func Test_asyncProducer_InputAfterShutdownPanics(t *testing.T) {
	stub := newStubAsyncProducer()
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, newConfig())
	require.NoError(t, p.Close())
	recv(t, p.drainDone, "input drainer")

	panicked := make(chan any, 1)
	go func() {
		defer func() { panicked <- recover() }()
		p.Input() <- &sarama.ProducerMessage{Topic: "topic"}
	}()
	require.NotNil(t, recv(t, panicked, "Input after shutdown to panic"), "sending on Input after shutdown did not panic")
}

func Test_asyncProducer_UnderlyingInputPanicCleansTracer(t *testing.T) {
	startAgent(t)
	config := ackConfig()

	stub := newStubAsyncProducer()
	close(stub.input)
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, config)
	tracer := newRecordingTracer("panic")
	ctx := pinpoint.NewContext(context.Background(), tracer)

	inputReturned := make(chan struct{})
	go func() {
		p.InputContext(ctx, &sarama.ProducerMessage{Topic: "topic"})
		close(inputReturned)
	}()
	recv(t, inputReturned, "wrapper input")
	recv(t, p.inputDone, "input forwarder")
	recv(t, tracer.ended, "panicked-send tracer")
	require.Equal(t, sarama.ErrShuttingDown, tracer.se.err, "the span event recorded the wrong verdict")
	requireSpanCount(t, p, 0)

	p.AsyncClose()
	requireChannelsClosed(t, p)
}

func Test_asyncProducer_ShutdownEndsRemainingTracer(t *testing.T) {
	startAgent(t)
	config := ackConfig()

	stub := newStubAsyncProducer()
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, config)
	tracer := newRecordingTracer("remaining")
	ctx := pinpoint.NewContext(context.Background(), tracer)

	p.InputContext(ctx, &sarama.ProducerMessage{Topic: "topic"})
	<-stub.input
	requireSpanCount(t, p, 1)
	p.AsyncClose()

	requireChannelsClosed(t, p)
	recv(t, tracer.ended, "remaining tracer")
	require.Equal(t, sarama.ErrShuttingDown, tracer.se.err, "the span event recorded the wrong verdict")
	requireSpanCount(t, p, 0)
	recv(t, p.drainDone, "input drainer")
}

// Close must return the undelivered messages as ProducerErrors, like raw
// sarama's Close, with every event drained through the wrapper.
func Test_asyncProducer_CloseCollectsErrors(t *testing.T) {
	config := newConfig()

	stub := newStubAsyncProducer()
	stub.onClose = nil
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
	startAgent(t)
	stub := newStubAsyncProducer()
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, nil)

	tracer := newRecordingTracer("a")
	msg := &sarama.ProducerMessage{Topic: "topic"}
	p.InputContext(pinpoint.NewContext(context.Background(), tracer), msg)

	require.Same(t, msg, recv(t, stub.input, "the underlying producer's message"), "the wrong message reached the underlying producer")

	// nil config means Return.Successes=false: the span must be ended
	// immediately instead of waiting for an ack that will never come.
	recv(t, tracer.ended, "span end")
	requireSpanCount(t, p, 0)
}

// The standard retry pattern re-sends the very message object taken off
// Errors(), headers and all. It arrives carrying the first attempt's Pinpoint
// context, so the retry is nested: no span event, no new headers, and the
// retry's ack matches nothing in the span map, which the first attempt's error
// ack already cleared.
func Test_asyncProducer_RetriedMessageIsNested(t *testing.T) {
	startAgent(t)
	config := ackConfig()

	stub := newStubAsyncProducer()
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, config)

	msg := &sarama.ProducerMessage{Topic: "topic"}

	first := newRecordingTracer("id-1")
	p.InputContext(pinpoint.NewContext(context.Background(), first), msg)
	<-stub.input
	stub.errors <- &sarama.ProducerError{Msg: msg, Err: sarama.ErrOutOfBrokers}
	<-p.Errors()
	recv(t, first.ended, "first attempt's span end")
	before := append([]sarama.RecordHeader(nil), msg.Headers...)

	retry := newRecordingTracer("id-2")
	p.InputContext(pinpoint.NewContext(context.Background(), retry), msg)
	<-stub.input

	assert.Equal(t, before, msg.Headers, "the retry neither appends nor replaces headers")
	requireSpanCount(t, p, 0)

	stub.successes <- msg
	<-p.Successes()
	select {
	case <-retry.ended:
		t.Fatal("the retry recorded a span it should not have")
	default:
	}
	requireSpanCount(t, p, 0)
}

// With Return.Errors off a failed message gets no ack at all, so the wrapper
// must not park tracers in the span map waiting for one: the span ends as the
// message is handed to sarama.
func Test_asyncProducer_NoErrorReturnsEndsSpansImmediately(t *testing.T) {
	startAgent(t)
	config := ackConfig()
	config.Producer.Return.Errors = false

	stub := newStubAsyncProducer()
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, config)

	tracer := newRecordingTracer("id-1")
	msg := &sarama.ProducerMessage{Topic: "topic"}
	p.InputContext(pinpoint.NewContext(context.Background(), tracer), msg)
	<-stub.input

	recv(t, tracer.ended, "span end")
	requireSpanCount(t, p, 0)
	for _, h := range msg.Headers {
		assert.NotEqual(t, HeaderAsyncSpanId, string(h.Key),
			"no async span id header must be added when acks cannot end the tracer")
	}
}

// Every message accepted before shutdown reaches sarama.
func Test_asyncProducer_ShutdownForwardsAcceptedMessages(t *testing.T) {
	startAgent(t)
	config := ackConfig()
	config.Producer.Return.Errors = true

	const messages = 16
	stub := newStubAsyncProducer()
	stub.onClose = nil
	// An unbuffered sarama input parks the forwarder on the first message,
	// with the rest buffered behind it in the wrapper, until the reads below.
	stub.input = make(chan *sarama.ProducerMessage)
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, config)

	for i := 0; i < messages; i++ {
		p.Input() <- &sarama.ProducerMessage{Topic: "topic"}
	}
	p.AsyncClose()

	for i := 0; i < messages; i++ {
		recv(t, stub.input, "accepted message")
	}

	close(stub.successes)
	close(stub.errors)
	requireChannelsClosed(t, p)
}

// takeInput is what gives an accepted message priority over the shutdown
// signal, from either input.
func Test_asyncProducer_takeInput(t *testing.T) {
	p := &asyncProducer{
		inputContext: make(chan *sarama.ProducerMessage, 1),
		input:        make(chan *sarama.ProducerMessage, 1),
	}

	_, ok := p.takeInput()
	assert.False(t, ok, "nothing accepted yet")

	traced := &sarama.ProducerMessage{Topic: "traced"}
	p.inputContext <- traced
	msg, ok := p.takeInput()
	require.True(t, ok)
	assert.Same(t, traced, msg)

	raw := &sarama.ProducerMessage{Topic: "raw"}
	p.input <- raw
	msg, ok = p.takeInput()
	require.True(t, ok)
	assert.Same(t, raw, msg)
}

// A closed underlying input cannot panic the host process.
func Test_sendAsyncProducerMessage_closedInput(t *testing.T) {
	input := make(chan *sarama.ProducerMessage)
	close(input)
	assert.False(t, sendAsyncProducerMessage(input, &sarama.ProducerMessage{}))
}

// Close must flush buffered messages through an underlying producer that only
// resumes receiving after shutdown has started.
func Test_asyncProducer_CloseDrainsBufferedMessagesThroughBackpressure(t *testing.T) {
	stub := newStubAsyncProducer()
	stub.input = make(chan *sarama.ProducerMessage)
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, newConfig())
	const count = 32
	messages := make([]*sarama.ProducerMessage, count)
	for i := range messages {
		messages[i] = &sarama.ProducerMessage{Topic: "topic"}
		p.InputContext(context.Background(), messages[i])
	}
	closed := make(chan error, 1)
	go func() { closed <- p.Close() }()
	recv(t, p.done, "shutdown signal")
	for _, want := range messages {
		require.Same(t, want, recv(t, stub.input, "accepted message"))
	}
	require.NoError(t, recv(t, closed, "Close"))
	recv(t, p.drainDone, "input drainer")
}

// sarama logs and ignores a nil message on Input(); the wrapper hands it on
// untouched. Tracing it dereferenced the nil in the input forwarder, whose
// death then dropped every later message in silence.
func Test_asyncProducer_NilMessageOnInputIsPassedThrough(t *testing.T) {
	startAgent(t)
	stub := newStubAsyncProducer()
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, newConfig())

	p.Input() <- nil
	p.Input() <- &sarama.ProducerMessage{Topic: "widgets"}

	select {
	case <-p.inputDone:
		t.Fatal("the input forwarder died on a nil message")
	case <-time.After(100 * time.Millisecond):
	}
	assert.Nil(t, recv(t, stub.input, "the nil message"), "the nil is forwarded for sarama to ignore")
	msg := recv(t, stub.input, "the message after the nil")
	require.NotNil(t, msg)
	assert.Equal(t, "widgets", msg.Topic, "the message after the nil is delivered")
}

// The same nil through InputContext, which traces in the caller's goroutine:
// it must not panic there either.
func Test_asyncProducer_NilMessageOnInputContextIsPassedThrough(t *testing.T) {
	startAgent(t)
	stub := newStubAsyncProducer()
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, newConfig())

	assert.NotPanics(t, func() { p.InputContext(t.Context(), nil) })
	assert.Nil(t, recv(t, stub.input, "the nil message"))
}
