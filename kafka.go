package pinpoint

import "strconv"

// KafkaConsumerMessage describes the consumed record a Kafka consumer span is
// started for. Broker is the address the application gave its plugin; when it
// gave none (""), the host the producer stamped on the record (HeaderHost)
// stands in, and "Unknown" when there is neither.
type KafkaConsumerMessage struct {
	Topic     string
	Partition int32
	Offset    int64
	Broker    string
}

// RpcName is the consumer span's name: "kafka://topic=t?partition=p&offset=o".
func (m KafkaConsumerMessage) RpcName() string {
	return "kafka://topic=" + m.Topic +
		"?partition=" + strconv.Itoa(int(m.Partition)) +
		"&offset=" + strconv.FormatInt(m.Offset, 10)
}

// NewKafkaConsumerTracer starts the span of a consumed Kafka record: the trace
// context is read from reader (the record headers), the broker is the span's
// end point and remote address, and the topic, partition and offset are its
// annotations. The sarama and confluent plugins both build their consumer
// span through it. The caller ends the span.
func NewKafkaConsumerTracer(operation string, msg KafkaConsumerMessage, reader DistributedTracingContextReader) Tracer {
	agent := GetAgent()
	// A disabled agent returns the noop tracer anyway; return it before
	// building the rpc name it would throw away.
	if !agent.Enable() {
		return NoopTracer()
	}

	tracer := agent.NewSpanTracerWithReader(operation, msg.RpcName(), reader)

	// A nil reader is a nil carrier, which NewSpanTracerWithReader tolerates;
	// this fallback has to as well.
	broker := msg.Broker
	if broker == "" && reader != nil {
		broker, _ = reader.Get(HeaderHost)
	}
	if broker == "" {
		broker = "Unknown"
	}

	span := tracer.Span()
	span.SetServiceType(ServiceTypeKafkaClient)
	span.SetRemoteAddress(broker)
	span.SetAcceptorHost(broker)
	span.SetEndPoint(broker)

	a := span.Annotations()
	a.AppendString(AnnotationKafkaTopic, msg.Topic)
	a.AppendInt(AnnotationKafkaPartition, msg.Partition)
	a.AppendLong(AnnotationKafkaOffset, msg.Offset)

	return tracer
}
