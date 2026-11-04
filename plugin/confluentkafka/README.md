# ppconfluentkafka
This package instruments the [confluentinc/confluent-kafka-go](https://github.com/confluentinc/confluent-kafka-go) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/confluentkafka/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/confluentkafka/v2"
```
confluent-kafka-go is a cgo binding to librdkafka, so `CGO_ENABLED=1` and a C compiler are required, as for the library itself.

## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/confluentkafka/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/confluentkafka/v2)

This package instruments Kafka consumers and producers.

### Consumer
To instrument a Kafka consumer, use ConsumeMessageContext.
To show the broker the consumer reads from on the pinpoint screen, pass it a context with the bootstrap servers, made by NewContext.
Without one, the span shows the broker the producer recorded on the message, or `Unknown` for a message from an uninstrumented producer.

``` go
ctx := ppconfluentkafka.NewContext(context.Background(), "localhost:9092")
for {
    msg, err := consumer.ReadMessage(-1)
    if err != nil {
        continue
    }
    ppconfluentkafka.ConsumeMessageContext(processMessage, ctx, msg)
}
```

ConsumeMessageContext records the handler call as a span event named after the handler function,
as the HTTP and gRPC server plugins do, and passes a context added pinpoint.Tracer to HandlerContextFunc.
In HandlerContextFunc, this tracer can be obtained by using the pinpoint.FromContext function;
calls traced with the context are recorded under the handler's event.

``` go
func processMessage(ctx context.Context, msg *kafka.Message) error {
    fmt.Printf("Message on %s: %s\n", msg.TopicPartition, string(msg.Value))
    return nil
}
```

The span ends when the handler returns, so call ConsumeMessageContext on the goroutine that processes the message;
in a worker pool, call it in the worker. See [Message queues](/doc/plugin_guide.md#message-queues).

[Full Example Source](/example/confluentkafka/consumer/consumer.go)

### Producer
To instrument a Kafka producer, use NewProducer and ProduceContext with the context containing the pinpoint.Tracer.

``` go
producer, err := ppconfluentkafka.NewProducer(&kafka.ConfigMap{"bootstrap.servers": "localhost:9092"})

func save(w http.ResponseWriter, r *http.Request) {
    deliveryChan := make(chan kafka.Event, 1)
    err := producer.ProduceContext(r.Context(), msg, deliveryChan)
    ...
    e := <-deliveryChan
}
```

The span event covers the enqueue, and a failed enqueue is recorded on it.
The delivery report goes where `Produce` sends it - the delivery channel, or `Events()` without one - untouched and in librdkafka's order.
A failed delivery arrives later, in that report, and is not recorded; record it yourself where you read the report:

``` go
if m := (<-deliveryChan).(*kafka.Message); m.TopicPartition.Error != nil {
    pinpoint.FromContext(r.Context()).Span().SetError(m.TopicPartition.Error)
}
```

`Produce` and `ProduceChannel` produce without tracing, since they carry no context.

A `*kafka.Producer` created elsewhere is wrapped with `WrapProducer(producer, conf)`;
the first `bootstrap.servers` entry of `conf` is the broker shown on the pinpoint screen.

[Full Example Source](/example/confluentkafka/producer/producer.go)
