// Package ppsaramaibm instruments the IBM/sarama package (https://github.com/IBM/sarama).
//
// This package instruments Kafka consumers and producers.
//
// To instrument a Kafka consumer, use ConsumeMessageContext.
// In order to display the kafka broker on the pinpoint screen,
// a context with broker addresses must be created and delivered using NewContext.
//
// ConsumePartition example:
//
//	ctx := ppsaramaibm.NewContext(context.Background(), broker)
//	pc, _ := consumer.ConsumePartition(topic, partition, offset)
//	for msg := range pc.Messages() {
//	  ppsaramaibm.ConsumeMessageContext(processMessage, ctx, msg)
//	}
//
// ConsumerGroupHandler example:
//
//	func (h exampleConsumerGroupHandler) ConsumeClaim(sess sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
//	  ctx := sess.Context()
//	  for msg := range claim.Messages() {
//	    _ = ppsaramaibm.ConsumeMessageContext(process, ctx, msg)
//	  }
//
// ConsumeMessageContext records the handler call as a span event named after
// the handler function, as the HTTP and gRPC server plugins do, and passes a
// context added pinpoint.Tracer to HandlerContextFunc. In HandlerContextFunc,
// this tracer can be obtained by using the pinpoint.FromContext function; calls
// traced with the context are recorded under the handler's event.
//
//	func process(ctx context.Context, msg *sarama.ConsumerMessage) error {
//	  fmt.Printf("Message topic:%q partition:%d offset:%d\n", msg.Topic, msg.Partition, msg.Offset)
//	  return save(ctx, msg) // traced under the handler's event
//
// To instrument a Kafka producer, use NewSyncProducer or NewAsyncProducer and
// send through SendMessageContext (or InputContext) with the context that
// carries the pinpoint.Tracer. SendMessage and Input produce without tracing.
//
//	config := sarama.NewConfig()
//	producer, err = ppsaramaibm.NewSyncProducer(brokers, config)
//	partition, offset, err := producer.SendMessageContext(r.Context(), msg)
package ppsaramaibm

import (
	"context"
	"errors"
	"reflect"
	"runtime"

	"github.com/IBM/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

// errNilConsumerMessage guards the Consume entry points: a nil message would
// panic inside the tracer on the consumer goroutine, propagate out of
// ConsumeClaim and kill the whole consumer-group session.
var errNilConsumerMessage = errors.New("ppsaramaibm: nil sarama.ConsumerMessage")

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
	// The handler call is an event of the span, named after the handler as
	// the HTTP and gRPC servers name theirs: what the handler records goes
	// under it, and a goroutine tracer forked during it - by the handler, or
	// by a plugin whose callback runs off this goroutine - has an event to
	// link to, where a span with none forked a noop that recorded nothing.
	if tracer.IsSampled() {
		defer tracer.NewSpanEvent(handlerName(handler)).EndSpanEvent()
	}

	err := handler(pinpoint.NewContext(ctx, tracer), msg)
	tracer.Span().SetError(err)
	return err
}

type distributedTracingContextReaderConsumer struct {
	msg *sarama.ConsumerMessage
}

// Get reports a record header carried with an empty value as present: a
// producer that wrote a blank Pinpoint-SpanID still describes a hop, and the
// trace continues through it. A value receiver: the one-pointer struct is
// handed to the tracer as an interface without an allocation per message.
func (m distributedTracingContextReaderConsumer) Get(key string) (string, bool) {
	for _, h := range m.msg.Headers {
		if h != nil && string(h.Key) == key {
			return string(h.Value), true
		}
	}
	return "", false
}

// handlerName is the handler's function name and "()", as
// pphttp.HandlerFuncName names an HTTP handler.
func handlerName(handler HandlerContextFunc) string {
	if fn := runtime.FuncForPC(reflect.ValueOf(handler).Pointer()); fn != nil {
		return fn.Name() + "()"
	}
	return "HandlerContextFunc()"
}

func makeRpcName(msg *sarama.ConsumerMessage) string {
	return consumerMessage(msg).RpcName()
}

func consumerMessage(msg *sarama.ConsumerMessage) pinpoint.KafkaConsumerMessage {
	return pinpoint.KafkaConsumerMessage{Topic: msg.Topic, Partition: msg.Partition, Offset: msg.Offset}
}

func newConsumerTracer(ctx context.Context, msg *sarama.ConsumerMessage) pinpoint.Tracer {
	// A nil context is a request with nothing in it; reading a value off it
	// panicked out of ConsumeClaim and ended the consumer group session.
	if ctx == nil {
		ctx = context.Background()
	}

	m := consumerMessage(msg)
	// Keep the Unknown fallback for an empty or mistyped context value: a
	// panic here propagates out of ConsumeClaim and kills the consumer. An
	// empty slice is still a value, so the host header is not consulted.
	if v := ctx.Value(contextKey); v != nil {
		m.Broker = "Unknown"
		if addrs, ok := v.([]string); ok && len(addrs) > 0 {
			m.Broker = addrs[0]
		}
	}
	return pinpoint.NewKafkaConsumerTracer("Sarama Consumer Invocation", m, distributedTracingContextReaderConsumer{msg})
}
