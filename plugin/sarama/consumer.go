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
// To instrument a Kafka producer, use NewSyncProducer or NewAsyncProducer.
//
//	config := sarama.NewConfig()
//	producer, err = ppsarama.NewSyncProducer(brokers, config)
//
// It is necessary to pass the context containing the pinpoint.Tracer
// to sarama.SyncProducer (or sarama.AsyncProducer) using WithContext function.
//
//	ppsarama.WithContext(pinpoint.NewContext(context.Background(), tracer), producer)
//	partition, offset, err := producer.SendMessage(msg)
//
// The WithContext function() function is not thread-safe, so use the SendMessageContext function() if you have a data trace.
//
//	partition, offset, err := producer.SendMessageContext(r.Context(), msg)
package ppsarama

import (
	"context"
	"errors"
	"strconv"

	"github.com/Shopify/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// errNilConsumerMessage guards the Consume entry points: a nil message would
// panic inside the tracer on the consumer goroutine, propagate out of
// ConsumeClaim and kill the whole consumer-group session.
var errNilConsumerMessage = errors.New("ppsarama: nil sarama.ConsumerMessage")

const contextKey = "ppsarama.broker.address"

// NewContext returns a new Context that contains the given broker addresses.
func NewContext(ctx context.Context, addrs []string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextKey, addrs)
}

// ConsumerMessage is deprecated.
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

// WrapConsumerMessage is deprecated.
// WrapConsumerMessage wraps a sarama.ConsumerMessage
// and creates a pinpoint.Tracer that instruments the sarama.ConsumerMessage.
// The tracer extracts the pinpoint header from message header,
// and then creates a span that initiates or continues the transaction.
func WrapConsumerMessage(msg *sarama.ConsumerMessage) *ConsumerMessage {
	return wrapConsumerMessage(context.Background(), msg)
}

func wrapConsumerMessage(ctx context.Context, msg *sarama.ConsumerMessage) *ConsumerMessage {
	return &ConsumerMessage{msg, newConsumerTracer(ctx, msg)}
}

// HandlerFunc is deprecated.
type HandlerFunc func(msg *ConsumerMessage) error

// ConsumeMessage is deprecated.
// ConsumeMessage creates a pinpoint.Tracer that instruments the sarama.ConsumerMessage.
// The tracer extracts the pinpoint header from message header,
// and then creates a span that initiates or continues the transaction.
// ConsumeMessage passes a ConsumerMessage having pinpoint.Tracer to HandlerFunc.
func ConsumeMessage(handler HandlerFunc, msg *sarama.ConsumerMessage) error {
	if msg == nil {
		return errNilConsumerMessage
	}
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

func (m *distributedTracingContextReaderConsumer) Get(key string) string {
	for _, h := range m.msg.Headers {
		if h != nil && string(h.Key) == key {
			return string(h.Value)
		}
	}
	return ""
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

	reader := &distributedTracingContextReaderConsumer{msg}
	tracer := agent.NewSpanTracerWithReader("Sarama Consumer Invocation", makeRpcName(msg), reader)

	// Keep the Unknown fallback for an empty or mistyped context value: a
	// panic here propagates out of ConsumeClaim and kills the consumer.
	brokerAddr := "Unknown"
	if v := ctx.Value(contextKey); v != nil {
		if addrs, ok := v.([]string); ok && len(addrs) > 0 {
			brokerAddr = addrs[0]
		}
	} else if host := reader.Get(pinpoint.HeaderHost); host != "" {
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

// PartitionConsumer is deprecated.
type PartitionConsumer struct {
	sarama.PartitionConsumer
	messages chan *ConsumerMessage
}

// Messages is deprecated.
func (pc *PartitionConsumer) Messages() <-chan *ConsumerMessage {
	return pc.messages
}

// Close is deprecated. Close mirrors raw sarama's Close, which drains the
// message channel: the forwarder may be parked sending a message it already
// took off sarama's channel to a caller that stopped receiving, and without
// the drain that goroutine - and the message's never-ended span - would leak
// for the life of the process.
func (pc *PartitionConsumer) Close() error {
	err := pc.PartitionConsumer.Close()
	for msg := range pc.messages {
		msg.Tracer().EndSpan()
	}
	return err
}

// WrapPartitionConsumer is deprecated.
func WrapPartitionConsumer(pc sarama.PartitionConsumer) *PartitionConsumer {
	return wrapPartitionConsumer(context.Background(), pc)
}

func wrapPartitionConsumer(ctx context.Context, pc sarama.PartitionConsumer) *PartitionConsumer {
	wrapped := &PartitionConsumer{
		PartitionConsumer: pc,
		messages:          make(chan *ConsumerMessage),
	}

	go func() {
		for msg := range pc.Messages() {
			wrapped.messages <- wrapConsumerMessage(ctx, msg)
		}
		close(wrapped.messages)
	}()

	return wrapped
}

// Consumer is deprecated.
type Consumer struct {
	sarama.Consumer
	addrs []string
}

// ConsumePartition is deprecated.
func (c *Consumer) ConsumePartition(topic string, partition int32, offset int64) (*PartitionConsumer, error) {
	pc, err := c.Consumer.ConsumePartition(topic, partition, offset)
	if err != nil {
		return nil, err
	}
	return wrapPartitionConsumer(NewContext(context.Background(), c.addrs), pc), nil
}

// NewConsumer is deprecated.
func NewConsumer(addrs []string, config *sarama.Config) (*Consumer, error) {
	consumer, err := sarama.NewConsumer(addrs, config)
	if err != nil {
		return nil, err
	}

	return &Consumer{Consumer: consumer, addrs: addrs}, nil
}
