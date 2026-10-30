package ppconfluentkafka

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"strings"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

// errNilConsumerMessage guards ConsumeMessageContext: ReadMessage returns a nil
// message with its error, and a nil message would panic inside the tracer.
var errNilConsumerMessage = errors.New("ppconfluentkafka: nil kafka.Message")

// contextKeyType makes the broker key unforgeable outside this package.
type contextKeyType struct{}

var contextKey = contextKeyType{}

// NewContext returns a new Context that contains the given bootstrap servers,
// in the "bootstrap.servers" format ("host1:9092,host2:9092").
func NewContext(ctx context.Context, bootstrapServers string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextKey, firstBroker(bootstrapServers))
}

// firstBroker is the broker shown on the pinpoint screen: the first entry of
// a "bootstrap.servers" list, or Unknown when there is none.
func firstBroker(bootstrapServers string) string {
	first, _, _ := strings.Cut(bootstrapServers, ",")
	if first = strings.TrimSpace(first); first == "" {
		return "Unknown"
	}
	return first
}

type HandlerContextFunc func(context.Context, *kafka.Message) error

// ConsumeMessageContext creates a pinpoint.Tracer that instruments the kafka.Message.
// The tracer extracts the pinpoint header from message header,
// and then creates a span that initiates or continues the transaction.
// ConsumeMessageContext passes a context added pinpoint.Tracer to HandlerContextFunc.
func ConsumeMessageContext(handler HandlerContextFunc, ctx context.Context, msg *kafka.Message) error {
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

// handlerName is the handler's function name and "()", as
// pphttp.HandlerFuncName names an HTTP handler.
func handlerName(handler HandlerContextFunc) string {
	if fn := runtime.FuncForPC(reflect.ValueOf(handler).Pointer()); fn != nil {
		return fn.Name() + "()"
	}
	return "HandlerContextFunc()"
}

func makeRpcName(msg *kafka.Message) string {
	return consumerMessage(msg).RpcName()
}

func consumerMessage(msg *kafka.Message) pinpoint.KafkaConsumerMessage {
	return pinpoint.KafkaConsumerMessage{
		Topic:     topicOf(msg),
		Partition: msg.TopicPartition.Partition,
		Offset:    int64(msg.TopicPartition.Offset),
	}
}

func newConsumerTracer(ctx context.Context, msg *kafka.Message) pinpoint.Tracer {
	// A nil context is a request with nothing in it; reading a value off it
	// panicked in the consumer.
	if ctx == nil {
		ctx = context.Background()
	}

	m := consumerMessage(msg)
	// NewContext always stores a broker (Unknown for an empty list), so a
	// missing value means the application gave none.
	m.Broker, _ = ctx.Value(contextKey).(string)
	return pinpoint.NewKafkaConsumerTracer("Kafka Consumer Invocation", m, headerReader{msg})
}
