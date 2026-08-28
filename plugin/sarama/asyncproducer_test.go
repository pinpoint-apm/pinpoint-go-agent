package ppsarama

import (
	"errors"
	"testing"

	"github.com/Shopify/sarama"
)

type stubAsyncProducer struct {
	sarama.AsyncProducer
	input     chan *sarama.ProducerMessage
	successes chan *sarama.ProducerMessage
	errors    chan *sarama.ProducerError
}

func (s *stubAsyncProducer) AsyncClose()                               {}
func (s *stubAsyncProducer) Close() error                              { return nil }
func (s *stubAsyncProducer) Input() chan<- *sarama.ProducerMessage     { return s.input }
func (s *stubAsyncProducer) Successes() <-chan *sarama.ProducerMessage { return s.successes }
func (s *stubAsyncProducer) Errors() <-chan *sarama.ProducerError      { return s.errors }

func newStubAsyncProducer() *stubAsyncProducer {
	return &stubAsyncProducer{
		input:     make(chan *sarama.ProducerMessage, 8),
		successes: make(chan *sarama.ProducerMessage, 8),
		errors:    make(chan *sarama.ProducerError, 8),
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
	for i := range sent {
		sent[i] = &sarama.ProducerMessage{Topic: "topic"}
		p.Input() <- sent[i]
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
