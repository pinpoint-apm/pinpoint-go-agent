// Package ppsarama instruments the Shopify/sarama package (https://github.com/Shopify/sarama).
//
// This package instruments Kafka consumers and producers.
//
// To instrument a Kafka consumer, use ConsumeMessageContext.
// In order to display the kafka broker on the pinpoint screen,
// a context with broker addresses must be created and delivered using NewContext.
//
// ConsumePartition example:
//
//	ctx := ppsarama.NewContext(context.Background(), broker)
//	pc, _ := consumer.ConsumePartition(topic, partition, offset)
//	for msg := range pc.Messages() {
//	  ppsarama.ConsumeMessageContext(processMessage, ctx, msg)
//	}
//
// ConsumerGroupHandler example:
//
//	func (h exampleConsumerGroupHandler) ConsumeClaim(sess sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
//	  ctx := sess.Context()
//	  for msg := range claim.Messages() {
//	    _ = ppsarama.ConsumeMessageContext(process, ctx, msg)
//	  }
//
// ConsumeMessageContext passes a context added pinpoint.Tracer to HandlerContextFunc.
// In HandlerContextFunc, this tracer can be obtained by using the pinpoint.FromContext function.
//
//	func process(ctx context.Context, msg *sarama.ConsumerMessage) error {
//	  tracer := pinpoint.FromContext(ctx)
//	  defer tracer.NewSpanEvent("process").EndSpanEvent()
//
//	  fmt.Printf("Message topic:%q partition:%d offset:%d\n", msg.Topic, msg.Partition, msg.Offset)
//
// To instrument a Kafka producer, use NewSyncProducer or NewAsyncProducer and
// send through SendMessageContext (or InputContext) with the context that
// carries the pinpoint.Tracer. SendMessage and Input produce without tracing.
//
//	config := sarama.NewConfig()
//	producer, err = ppsarama.NewSyncProducer(brokers, config)
//	partition, offset, err := producer.SendMessageContext(r.Context(), msg)
package ppsarama

import (
	"context"
	"errors"
	"strconv"

	"github.com/Shopify/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

// errNilConsumerMessage guards the Consume entry points: a nil message would
// panic inside the tracer on the consumer goroutine, propagate out of
// ConsumeClaim and kill the whole consumer-group session.
var errNilConsumerMessage = errors.New("ppsarama: nil sarama.ConsumerMessage")

// contextKeyType makes the broker-address key unforgeable outside this
// package: an untyped string constant is a key any other package can
// produce by accident, and a collision on it costs the consumer span its
// broker address (staticcheck SA1029).
type contextKeyType struct{}

var contextKey = contextKeyType{}

// NewContext returns a new Context that contains the given broker addresses.
func NewContext(ctx context.Context, addrs []string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextKey, addrs)
}

type HandlerContextFunc func(context.Context, *sarama.ConsumerMessage) error

// ConsumeMessageContext creates a pinpoint.Tracer that instruments the sarama.ConsumerMessage.
// The tracer extracts the pinpoint header from message header,
// and then creates a span that initiates or continues the transaction.
// ConsumeMessageContext passes a context added pinpoint.Tracer to HandlerContextFunc.
func ConsumeMessageContext(handler HandlerContextFunc, ctx context.Context, msg *sarama.ConsumerMessage) error {
	if msg == nil {
		return errNilConsumerMessage
	}
	tracer := newConsumerTracer(ctx, msg)
	defer tracer.EndSpan()

	err := handler(pinpoint.NewContext(ctx, tracer), msg)
	tracer.Span().SetError(err)
	return err
}

type distributedTracingContextReaderConsumer struct {
	msg *sarama.ConsumerMessage
}

// Get reports a record header carried with an empty value as present: a
// producer that wrote a blank Pinpoint-SpanID still describes a hop, and the
// trace continues through it.
func (m *distributedTracingContextReaderConsumer) Get(key string) (string, bool) {
	for _, h := range m.msg.Headers {
		if h != nil && string(h.Key) == key {
			return string(h.Value), true
		}
	}
	return "", false
}

func makeRpcName(msg *sarama.ConsumerMessage) string {
	return "kafka://topic=" + msg.Topic +
		"?partition=" + strconv.Itoa(int(msg.Partition)) +
		"&offset=" + strconv.FormatInt(msg.Offset, 10)
}

func newConsumerTracer(ctx context.Context, msg *sarama.ConsumerMessage) pinpoint.Tracer {
	agent := pinpoint.GetAgent()
	// A disabled agent returns the noop tracer anyway; return it before
	// building the rpc name it would throw away.
	if !agent.Enable() {
		return pinpoint.NoopTracer()
	}

	// A nil context is a request with nothing in it; reading a value off it
	// panicked out of ConsumeClaim and ended the consumer group session.
	if ctx == nil {
		ctx = context.Background()
	}

	reader := &distributedTracingContextReaderConsumer{msg}
	tracer := agent.NewSpanTracerWithReader("Sarama Consumer Invocation", makeRpcName(msg), reader)

	// Keep the Unknown fallback for an empty or mistyped context value: a
	// panic here propagates out of ConsumeClaim and kills the consumer.
	brokerAddr := "Unknown"
	if v := ctx.Value(contextKey); v != nil {
		if addrs, ok := v.([]string); ok && len(addrs) > 0 {
			brokerAddr = addrs[0]
		}
	} else if host, _ := reader.Get(pinpoint.HeaderHost); host != "" {
		brokerAddr = host
	}

	span := tracer.Span()
	span.SetServiceType(pinpoint.ServiceTypeKafkaClient)
	span.SetRemoteAddress(brokerAddr)
	span.SetAcceptorHost(brokerAddr)
	span.SetEndPoint(brokerAddr)

	a := span.Annotations()
	a.AppendString(pinpoint.AnnotationKafkaTopic, msg.Topic)
	a.AppendInt(pinpoint.AnnotationKafkaPartition, msg.Partition)
	// The offset is an int64; casting to int32 truncated it past 2^31.
	a.AppendLong(pinpoint.AnnotationKafkaOffset, msg.Offset)

	return tracer
}
