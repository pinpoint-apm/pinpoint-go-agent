package ppsarama

import (
	"testing"
	"time"

	"github.com/Shopify/sarama"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	select {
	case msg := <-stub.input:
		assert.Nil(t, msg, "the nil is forwarded for sarama to ignore")
	case <-time.After(2 * time.Second):
		t.Fatal("the nil message never reached sarama")
	}
	select {
	case msg := <-stub.input:
		require.NotNil(t, msg)
		assert.Equal(t, "widgets", msg.Topic, "the message after the nil is delivered")
	case <-time.After(2 * time.Second):
		t.Fatal("the message after the nil never reached sarama")
	}
}

// The same nil through InputContext, which traces in the caller's goroutine:
// it must not panic there either.
func Test_asyncProducer_NilMessageOnInputContextIsPassedThrough(t *testing.T) {
	startAgent(t)
	stub := newStubAsyncProducer()
	p := wrapAsyncProducer(stub, []string{"broker:9092"}, newConfig())

	assert.NotPanics(t, func() { p.InputContext(t.Context(), nil) })
	select {
	case msg := <-stub.input:
		assert.Nil(t, msg)
	case <-time.After(2 * time.Second):
		t.Fatal("the nil message never reached sarama")
	}
}
