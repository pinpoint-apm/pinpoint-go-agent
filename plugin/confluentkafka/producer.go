// Package ppconfluentkafka instruments the confluentinc/confluent-kafka-go
// package (https://github.com/confluentinc/confluent-kafka-go).
//
// This package instruments Kafka consumers and producers.
//
// To instrument a Kafka consumer, use ConsumeMessageContext.
// In order to display the kafka broker on the pinpoint screen,
// a context with the bootstrap servers must be created and delivered using NewContext.
//
//	ctx := ppconfluentkafka.NewContext(context.Background(), "localhost:9092")
//	for {
//	  msg, err := consumer.ReadMessage(-1)
//	  ...
//	  ppconfluentkafka.ConsumeMessageContext(process, ctx, msg)
//	}
//
// ConsumeMessageContext passes a context added pinpoint.Tracer to HandlerContextFunc.
// In HandlerContextFunc, this tracer can be obtained by using the pinpoint.FromContext function.
//
//	func process(ctx context.Context, msg *kafka.Message) error {
//	  tracer := pinpoint.FromContext(ctx)
//	  defer tracer.NewSpanEvent("process").EndSpanEvent()
//	  ...
//
// To instrument a Kafka producer, use NewProducer and ProduceContext.
//
//	producer, err := ppconfluentkafka.NewProducer(&kafka.ConfigMap{"bootstrap.servers": "localhost:9092"})
//	err = producer.ProduceContext(ctx, msg, deliveryChan)
package ppconfluentkafka

import (
	"context"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

// Producer wraps the kafka.Producer and provides the additional function ProduceContext for trace.
type Producer struct {
	*kafka.Producer
	broker string
}

// NewProducer wraps kafka.NewProducer and returns a Producer ready to instrument.
// The broker shown on the pinpoint screen is the first entry of "bootstrap.servers".
func NewProducer(conf *kafka.ConfigMap) (*Producer, error) {
	producer, err := kafka.NewProducer(conf)
	if err != nil {
		return nil, err
	}
	return WrapProducer(producer, conf), nil
}

// WrapProducer wraps a *kafka.Producer created elsewhere the way NewProducer
// wraps the one it creates; conf is the producer's configuration, whose
// first "bootstrap.servers" entry the span events report as the broker (a
// nil conf or a missing entry: "Unknown", as NewProducer records it). The
// compile-time instrumentation tool uses it from its kafka.NewProducer hook,
// together with the error kafka.NewProducer returned: a nil producer stays
// nil, so a caller that checks the wrapper for nil is not handed one whose
// every method nil-derefs inside the bindings.
func WrapProducer(producer *kafka.Producer, conf *kafka.ConfigMap) *Producer {
	if producer == nil {
		return nil
	}
	servers := ""
	if conf != nil {
		if v, err := conf.Get("bootstrap.servers", ""); err == nil {
			if s, ok := v.(string); ok {
				servers = s
			}
		}
	}
	return &Producer{Producer: producer, broker: firstBroker(servers)}
}

// ProduceContext produces a given message with tracer context, as kafka.Producer.Produce does.
//
// The span event covers the enqueue, and the delivery report goes where
// Produce sends it - deliveryChan, or Events() without one - untouched and in
// librdkafka's order.
func (p *Producer) ProduceContext(ctx context.Context, msg *kafka.Message, deliveryChan chan kafka.Event) error {
	// A disabled agent traces nothing and injects nothing - not even the
	// unsampled marker - so the message headers are left untouched. A nested
	// message (isNested) is sent as it is for the same reason.
	if msg == nil || !pinpoint.GetAgent().Enable() || isNested(msg) {
		return p.Producer.Produce(msg, deliveryChan)
	}

	// The report is not intercepted to end the span event on it: that took a
	// goroutine per message, which reordered the reports on a shared channel
	// - and an application tracking the last delivered offset by them - and
	// could still be sending after Flush returned, into a channel the
	// application had closed.
	tracer := newProducerTracer(pinpoint.FromContext(ctx), p.broker, msg)
	defer tracer.EndSpanEvent()
	err := p.Producer.Produce(msg, deliveryChan)
	tracer.SpanEvent().SetError(err)
	return err
}

func newProducerTracer(tracer pinpoint.Tracer, broker string, msg *kafka.Message) pinpoint.Tracer {
	tracer.NewSpanEvent("kafka.Producer.Produce")
	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeKafkaClient)
	se.Annotations().AppendString(pinpoint.AnnotationKafkaTopic, topicOf(msg))
	se.SetDestination(broker)
	tracer.Inject(&headerWriter{msg: msg})
	return tracer
}

// isNested reports whether msg already carries a Pinpoint trace context - an
// outer instrumented layer, or the retry pattern re-sending the same message
// object. The producer then records no span event and writes no header.
func isNested(msg *kafka.Message) bool {
	r := headerReader{msg}
	for _, key := range []string{pinpoint.HeaderTraceId, pinpoint.HeaderSampled} {
		if _, ok := r.Get(key); ok {
			return true
		}
	}
	return false
}

type headerWriter struct {
	msg *kafka.Message
	// grown is set once the slice was replaced for the injected headers.
	grown bool
}

// Set appends the header into a slice of the message's own: the first write
// copies the headers into a new array, so nothing lands in spare capacity the
// application's slice may share with another message.
func (w *headerWriter) Set(key string, value string) {
	if !w.grown {
		const injectedHeaders = 8
		grown := make([]kafka.Header, len(w.msg.Headers), len(w.msg.Headers)+injectedHeaders)
		copy(grown, w.msg.Headers)
		w.msg.Headers = grown
		w.grown = true
	}
	w.msg.Headers = append(w.msg.Headers, kafka.Header{Key: key, Value: []byte(value)})
}

// headerReader reads the record headers of a message. A header carried with an
// empty value is present: a producer that wrote a blank Pinpoint-SpanID still
// describes a hop, and the trace continues through it.
type headerReader struct {
	msg *kafka.Message
}

func (r headerReader) Get(key string) (string, bool) {
	for _, h := range r.msg.Headers {
		if h.Key == key {
			return string(h.Value), true
		}
	}
	return "", false
}

func topicOf(msg *kafka.Message) string {
	if msg.TopicPartition.Topic == nil {
		return ""
	}
	return *msg.TopicPartition.Topic
}
