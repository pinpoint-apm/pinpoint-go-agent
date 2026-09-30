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
In order to display the kafka broker on the pinpoint screen, a context with the bootstrap servers must be created and delivered using NewContext.

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

ConsumeMessageContext passes a context added pinpoint.Tracer to HandlerContextFunc.
In HandlerContextFunc, this tracer can be obtained by using the pinpoint.FromContext function.

``` go
func processMessage(ctx context.Context, msg *kafka.Message) error {
    tracer := pinpoint.FromContext(ctx)
    defer tracer.NewSpanEvent("processMessage").EndSpanEvent()

    fmt.Printf("Message on %s: %s\n", msg.TopicPartition, string(msg.Value))
    return nil
}
```
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

[Full Example Source](/example/confluentkafka/producer/producer.go)
