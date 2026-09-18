package ppconfluentkafka

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/pinpoint-apm/pinpoint-go-agent"
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

	err := handler(pinpoint.NewContext(ctx, tracer), msg)
	tracer.Span().SetError(err)
	return err
}

func makeRpcName(msg *kafka.Message) string {
	return "kafka://topic=" + topicOf(msg) +
		"?partition=" + strconv.Itoa(int(msg.TopicPartition.Partition)) +
		"&offset=" + strconv.FormatInt(int64(msg.TopicPartition.Offset), 10)
}

func newConsumerTracer(ctx context.Context, msg *kafka.Message) pinpoint.Tracer {
	agent := pinpoint.GetAgent()
	if !agent.Enable() {
		return pinpoint.NoopTracer()
	}

	reader := headerReader{msg}
	tracer := agent.NewSpanTracerWithReader("Kafka Consumer Invocation", makeRpcName(msg), reader)

	broker := "Unknown"
	if v, ok := ctx.Value(contextKey).(string); ok {
		broker = v
	} else if host, _ := reader.Get(pinpoint.HeaderHost); host != "" {
		broker = host
	}

	span := tracer.Span()
	span.SetServiceType(pinpoint.ServiceTypeKafkaClient)
	span.SetRemoteAddress(broker)
	span.SetAcceptorHost(broker)
	span.SetEndPoint(broker)

	a := span.Annotations()
	a.AppendString(pinpoint.AnnotationKafkaTopic, topicOf(msg))
	a.AppendInt(pinpoint.AnnotationKafkaPartition, msg.TopicPartition.Partition)
	a.AppendLong(pinpoint.AnnotationKafkaOffset, int64(msg.TopicPartition.Offset))

	return tracer
}
