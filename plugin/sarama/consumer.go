// Package ppsarama instruments the Shopify/sarama package (https://github.com/Shopify/sarama).
//
// This package instruments Kafka consumers and producers.
// To instrument a Kafka consumer, use ConsumeMessage or ConsumeMessageContext.
//
//  pc, _ := consumer.ConsumePartition(topic, partition, offset)
//  for msg := range pc.Messages() {
//    ppsarama.ConsumeMessage(processMessage, msg)
//  }
//
// ConsumeMessage wraps a sarama.ConsumerMessage and passes a ConsumerMessage having pinpoint.Tracer to HandlerFunc.
// In HandlerFunc, this tracer can be obtained by using the ConsumerMessage.Tracer function.
//
//  func processMessage(msg *ppsarama.ConsumerMessage) error {
//    tracer := msg.Tracer()
//    defer tracer.NewSpanEvent("processMessage").EndSpanEvent()
//
//    fmt.Printf("Message topic:%q partition:%d offset:%d\n", msg.Topic, msg.Partition, msg.Offset)
//    fmt.Println("retrieving message: ", string(msg.Value))
//
// For sarama.ConsumerGroupHandler, ConsumeMessageContext is provided.
//
//  func (h exampleConsumerGroupHandler) ConsumeClaim(sess sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
//    ctx := sess.Context()
//    for msg := range claim.Messages() {
//      _ = ppsarama.ConsumeMessageContext(process, ctx, msg)
//    }
//
// ConsumeMessageContext passes a context added pinpoint.Tracer to HandlerContextFunc.
// In HandlerContextFunc, this tracer can be obtained by using the pinpoint.FromContext function.
//
//  func process(ctx context.Context, msg *sarama.ConsumerMessage) error {
//    tracer := pinpoint.FromContext(ctx)
//    defer tracer.NewSpanEvent("process").EndSpanEvent()
//
//    fmt.Printf("Message topic:%q partition:%d offset:%d\n", msg.Topic, msg.Partition, msg.Offset)
//
// To instrument a Kafka producer, use NewSyncProducer or NewAsyncProducer.
//
//	config := sarama.NewConfig()
//	producer, err = ppsarama.NewSyncProducer(brokers, config)
//
// It is necessary to pass the context containing the pinpoint.Tracer
// to sarama.SyncProducer (or sarama.AsyncProducer) using WithContext function.
//
//  ppsarama.WithContext(pinpoint.NewContext(context.Background(), tracer), producer)
//  partition, offset, err := producer.SendMessage(msg)
package ppsarama

import (
	"bytes"
	"context"
	"strconv"

	"github.com/Shopify/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

type ConsumerMessage struct {
	*sarama.ConsumerMessage
	tracer pinpoint.Tracer
}

// SpanTracer is deprecated. Use Tracer.
func (c *ConsumerMessage) SpanTracer() pinpoint.Tracer {
	return c.tracer
}

// Tracer returns the pinpoint.Tracer.
func (c *ConsumerMessage) Tracer() pinpoint.Tracer {
	return c.tracer
}

// WrapConsumerMessage wraps a sarama.ConsumerMessage
// and creates a pinpoint.Tracer that instruments the sarama.ConsumerMessage.
// The tracer extracts the pinpoint header from message header,
// and then creates a span that initiates or continues the transaction.
func WrapConsumerMessage(msg *sarama.ConsumerMessage) *ConsumerMessage {
	return &ConsumerMessage{msg, newConsumerTracer(msg)}
}

type HandlerFunc func(msg *ConsumerMessage) error

// ConsumeMessage creates a pinpoint.Tracer that instruments the sarama.ConsumerMessage.
// The tracer extracts the pinpoint header from message header,
// and then creates a span that initiates or continues the transaction.
// ConsumeMessage passes a ConsumerMessage having pinpoint.Tracer to HandlerFunc.
func ConsumeMessage(handler HandlerFunc, msg *sarama.ConsumerMessage) error {
	wrapped := WrapConsumerMessage(msg)
	defer wrapped.Tracer().EndSpan()

	err := handler(wrapped)
	wrapped.Tracer().Span().SetError(err)
	return err
}

type HandlerContextFunc func(context.Context, *sarama.ConsumerMessage) error

// ConsumeMessageContext creates a pinpoint.Tracer that instruments the sarama.ConsumerMessage.
// The tracer extracts the pinpoint header from message header,
// and then creates a span that initiates or continues the transaction.
// ConsumeMessageContext passes a context added pinpoint.Tracer to HandlerContextFunc.
func ConsumeMessageContext(handler HandlerContextFunc, ctx context.Context, msg *sarama.ConsumerMessage) error {
	tracer := newConsumerTracer(msg)
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

func newConsumerTracer(msg *sarama.ConsumerMessage) pinpoint.Tracer {
	var tracer pinpoint.Tracer

	agent := pinpoint.GetAgent()
	reader := &distributedTracingContextReaderConsumer{msg}
	tracer = agent.NewSpanTracerWithReader("Sarama Consumer Invocation", makeRpcName(msg), reader)

	tracer.Span().SetServiceType(pinpoint.ServiceTypeKafkaClient)
	a := tracer.Span().Annotations()
	a.AppendString(pinpoint.AnnotationKafkaTopic, msg.Topic)
	a.AppendInt(pinpoint.AnnotationKafkaPartition, msg.Partition)
	a.AppendInt(pinpoint.AnnotationKafkaOffset, int32(msg.Offset))

	return tracer
}

// PartitionConsumer is deprecated.
type PartitionConsumer struct {
	sarama.PartitionConsumer
	messages chan *ConsumerMessage
}

// Messages is deprecated.
func (pc *PartitionConsumer) Messages() <-chan *ConsumerMessage {
	return pc.messages
}

// WrapPartitionConsumer is deprecated.
func WrapPartitionConsumer(pc sarama.PartitionConsumer) *PartitionConsumer {
	wrapped := &PartitionConsumer{
		PartitionConsumer: pc,
		messages:          make(chan *ConsumerMessage),
	}

	go func() {
		for msg := range pc.Messages() {
			wrapped.messages <- WrapConsumerMessage(msg)
		}
		close(wrapped.messages)
	}()

	return wrapped
}

// Consumer is deprecated.
type Consumer struct {
	sarama.Consumer
}

// ConsumePartition is deprecated.
func (c *Consumer) ConsumePartition(topic string, partition int32, offset int64) (*PartitionConsumer, error) {
	pc, err := c.Consumer.ConsumePartition(topic, partition, offset)
	if err != nil {
		return nil, err
	}
	return WrapPartitionConsumer(pc), nil
}

// NewConsumer is deprecated.
func NewConsumer(addrs []string, config *sarama.Config) (*Consumer, error) {
	consumer, err := sarama.NewConsumer(addrs, config)
	if err != nil {
		return nil, err
	}

	return &Consumer{Consumer: consumer}, nil
}
