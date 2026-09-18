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
	"github.com/pinpoint-apm/pinpoint-go-agent"
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
	servers, _ := conf.Get("bootstrap.servers", "")
	return &Producer{Producer: producer, broker: firstBroker(servers.(string))}, nil
}

// ProduceContext produces a given message with tracer context, as kafka.Producer.Produce does.
//
// With a deliveryChan, the span event is ended by the delivery report, so a
// failed delivery is recorded on it, and the report is then forwarded to
// deliveryChan. Without one, the delivery report goes to Events() as usual and
// the span event covers only the enqueue.
func (p *Producer) ProduceContext(ctx context.Context, msg *kafka.Message, deliveryChan chan kafka.Event) error {
	// A disabled agent traces nothing and injects nothing - not even the
	// unsampled marker - so the message headers are left untouched. A nested
	// message (isNested) is sent as it is for the same reason.
	if msg == nil || !pinpoint.GetAgent().Enable() || isNested(msg) {
		return p.Producer.Produce(msg, deliveryChan)
	}

	if deliveryChan == nil {
		tracer := newProducerTracer(pinpoint.FromContext(ctx), p.broker, msg)
		defer tracer.EndSpanEvent()
		err := p.Producer.Produce(msg, nil)
		tracer.SpanEvent().SetError(err)
		return err
	}

	// The report arrives on another goroutine, possibly after the caller's
	// span has ended, so the delivery is tracked on a goroutine tracer.
	tracer := newProducerTracer(pinpoint.FromContext(ctx).NewGoroutineTracer(), p.broker, msg)
	reports := make(chan kafka.Event, 1)
	if err := p.Producer.Produce(msg, reports); err != nil {
		endProducerTracer(tracer, err)
		return err
	}
	// ponytail: one goroutine per in-flight message. It parks forever if the
	// producer is closed without Flush, exactly like a message the application
	// itself would never get a report for. A single forwarder over a shared
	// report channel keyed on msg.Opaque is the upgrade path if that matters.
	go func() {
		e := <-reports
		var err error
		if m, ok := e.(*kafka.Message); ok {
			err = m.TopicPartition.Error
		}
		endProducerTracer(tracer, err)
		deliveryChan <- e
	}()
	return nil
}

func newProducerTracer(tracer pinpoint.Tracer, broker string, msg *kafka.Message) pinpoint.Tracer {
	tracer.NewSpanEvent("kafka.Producer.Produce")
	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeKafkaClient)
	se.Annotations().AppendString(pinpoint.AnnotationKafkaTopic, topicOf(msg))
	se.SetDestination(broker)
	tracer.Inject(&headerWriter{msg})
	return tracer
}

func endProducerTracer(tracer pinpoint.Tracer, err error) {
	tracer.SpanEvent().SetError(err)
	tracer.EndSpanEvent()
	tracer.EndSpan()
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
}

func (w *headerWriter) Set(key string, value string) {
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
