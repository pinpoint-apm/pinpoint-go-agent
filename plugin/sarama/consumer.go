package sarama

import (
	"bytes"
	"context"
	"strconv"

	"github.com/Shopify/sarama"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
)

const (
	serviceTypeKafkaClient   = 8660
	annotationKafkaTopic     = 140
	annotationKafkaPartition = 141
	annotationKafkaOffset    = 142
)

type ConsumerMessage struct {
	*sarama.ConsumerMessage
	tracer pinpoint.Tracer
}

// SpanTracer deprecated
func (c *ConsumerMessage) SpanTracer() pinpoint.Tracer {
	return c.tracer
}

func (c *ConsumerMessage) Tracer() pinpoint.Tracer {
	return c.tracer
}

func WrapConsumerMessage(msg *sarama.ConsumerMessage, agent pinpoint.Agent) *ConsumerMessage {
	return &ConsumerMessage{msg, newConsumerTracer(msg, agent)}
}

type HandlerFunc func(msg *ConsumerMessage) error

func ConsumeMessage(handler HandlerFunc, msg *sarama.ConsumerMessage, agent pinpoint.Agent) error {
	wrapped := WrapConsumerMessage(msg, agent)
	defer wrapped.Tracer().EndSpan()

	err := handler(wrapped)
	wrapped.Tracer().Span().SetError(err)
	return err
}

type HandlerContextFunc func(context.Context, *sarama.ConsumerMessage) error

func ConsumeMessageContext(handler HandlerContextFunc, ctx context.Context, msg *sarama.ConsumerMessage, agent pinpoint.Agent) error {
	tracer := newConsumerTracer(msg, agent)
	defer tracer.EndSpan()

	err := handler(pinpoint.NewContext(ctx, tracer), msg)
	tracer.Span().SetError(err)
	return err
}

type distributedTracingContextReaderConsumer struct {
	msg *sarama.ConsumerMessage
}

func (m *distributedTracingContextReaderConsumer) Get(key string) string {
	for _, h := range m.msg.Headers {
		if h != nil && string(h.Key) == key {
			return string(h.Value)
		}
	}
	return ""
}

func makeRpcName(msg *sarama.ConsumerMessage) string {
	var buf bytes.Buffer

	buf.WriteString("kafka://")
	buf.WriteString("topic=" + msg.Topic)
	buf.WriteString("?partition=" + strconv.Itoa(int(msg.Partition)))
	buf.WriteString("&offset=" + strconv.Itoa(int(msg.Offset)))

	return buf.String()
}

func newConsumerTracer(msg *sarama.ConsumerMessage, agent pinpoint.Agent) pinpoint.Tracer {
	var tracer pinpoint.Tracer

	if agent != nil {
		reader := &distributedTracingContextReaderConsumer{msg}
		tracer = agent.NewSpanTracerWithReader("Kafka Consumer Invocation", makeRpcName(msg), reader)
	} else {
		tracer = pinpoint.NoopTracer()
	}

	tracer.Span().SetServiceType(serviceTypeKafkaClient)
	a := tracer.Span().Annotations()
	a.AppendString(annotationKafkaTopic, msg.Topic)
	a.AppendInt(annotationKafkaPartition, msg.Partition)
	a.AppendInt(annotationKafkaOffset, int32(msg.Offset))

	return tracer
}

// PartitionConsumer deprecated
type PartitionConsumer struct {
	sarama.PartitionConsumer
	messages chan *ConsumerMessage
}

// Messages deprecated
func (pc *PartitionConsumer) Messages() <-chan *ConsumerMessage {
	return pc.messages
}

// WrapPartitionConsumer deprecated
func WrapPartitionConsumer(pc sarama.PartitionConsumer, agent pinpoint.Agent) *PartitionConsumer {
	wrapped := &PartitionConsumer{
		PartitionConsumer: pc,
		messages:          make(chan *ConsumerMessage),
	}

	go func() {
		for msg := range pc.Messages() {
			wrapped.messages <- WrapConsumerMessage(msg, agent)
		}
		close(wrapped.messages)
	}()

	return wrapped
}

// Consumer deprecated
type Consumer struct {
	sarama.Consumer
	agent pinpoint.Agent
}

// ConsumePartition deprecated
func (c *Consumer) ConsumePartition(topic string, partition int32, offset int64) (*PartitionConsumer, error) {
	pc, err := c.Consumer.ConsumePartition(topic, partition, offset)
	if err != nil {
		return nil, err
	}
	return WrapPartitionConsumer(pc, c.agent), nil
}

// NewConsumer deprecated
func NewConsumer(addrs []string, config *sarama.Config, agent pinpoint.Agent) (*Consumer, error) {
	consumer, err := sarama.NewConsumer(addrs, config)
	if err != nil {
		return nil, err
	}

	return &Consumer{Consumer: consumer, agent: agent}, nil
}
