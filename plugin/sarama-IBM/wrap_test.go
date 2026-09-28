package ppsaramaibm

import (
	"context"
	"testing"

	"github.com/IBM/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WrapSyncProducer wraps a producer created elsewhere exactly as
// NewSyncProducer wraps the one it creates, once: the wrapper traces on the
// context given and the underlying producer receives the message.
func TestWrapSyncProducer(t *testing.T) {
	startAgent(t)
	stub := &stubSyncProducer{}
	wrapped := WrapSyncProducer(stub, []string{"broker1:9092"}, newConfig())
	p, ok := wrapped.(*syncProducer)
	require.True(t, ok, "WrapSyncProducer must return the plugin's wrapper")
	assert.Equal(t, []string{"broker1:9092"}, p.addrs)
	assert.Same(t, wrapped, WrapSyncProducer(wrapped, nil, nil), "wrapping twice must return the same producer")

	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/produce")
	defer tracer.EndSpan()
	msg := &sarama.ProducerMessage{Topic: "widgets"}
	_, _, err := wrapped.SendMessageContext(pinpoint.NewContext(context.Background(), tracer), msg)
	require.NoError(t, err)
	require.Len(t, stub.sent, 1, "the underlying producer must receive the message")
	tid, ok := (&distributedTracingContextWriterProducer{msg: msg}).Get(pinpoint.HeaderTraceId)
	assert.True(t, ok, "the produced message is missing the trace header")
	assert.Equal(t, tracer.TransactionId().String(), tid)
}

// A wrapped producer without an address records no destination instead of
// panicking on the first message.
func TestWrapSyncProducer_WithoutAddress(t *testing.T) {
	startAgent(t)
	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/produce")
	defer tracer.EndSpan()
	stub := &stubSyncProducer{}
	_, _, err := WrapSyncProducer(stub, nil, nil).SendMessageContext(pinpoint.NewContext(context.Background(), tracer), &sarama.ProducerMessage{Topic: "widgets"})
	require.NoError(t, err)
	require.Len(t, stub.sent, 1)
}

// WrapAsyncProducer wraps a producer created elsewhere as NewAsyncProducer
// does, once, substituting sarama's default for a nil config, and shuts down
// like the wrapper it returns.
func TestWrapAsyncProducer(t *testing.T) {
	stub := newStubAsyncProducer()
	release := make(chan struct{})
	stub.onClose = func() {
		<-release
		close(stub.successes)
		close(stub.errors)
	}
	wrapped := WrapAsyncProducer(stub, []string{"broker1:9092"}, nil)
	p, ok := wrapped.(*asyncProducer)
	require.True(t, ok, "WrapAsyncProducer must return the plugin's wrapper")
	assert.Equal(t, []string{"broker1:9092"}, p.addrs)
	assert.NotNil(t, p.config, "a nil config must be replaced by sarama's default")
	assert.Same(t, wrapped, WrapAsyncProducer(wrapped, nil, nil), "wrapping twice must return the same producer")

	wrapped.AsyncClose()
	close(release)
	requireChannelsClosed(t, p)
	waitForClose(t, p.drainDone, "input drainer")
}

// sarama.NewSyncProducer and NewAsyncProducer return nil with their error, and
// the compile-time hook wraps whatever they return: a nil producer must come
// back as a nil interface, not a wrapper around nil - whose delivery goroutine,
// for the async one, crashed the process on the nil producer's channels.
func TestWrapProducers_NilStaysNil(t *testing.T) {
	assert.True(t, WrapSyncProducer(nil, nil, nil) == nil, "WrapSyncProducer(nil)")
	assert.True(t, WrapAsyncProducer(nil, nil, nil) == nil, "WrapAsyncProducer(nil)")
}
