# ppconfluentkafka
This package instruments the [confluentinc/confluent-kafka-go](https://github.com/confluentinc/confluent-kafka-go) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/confluentkafka
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/confluentkafka"
```
confluent-kafka-go is a cgo binding to librdkafka, so `CGO_ENABLED=1` and a C compiler are required, as for the library itself.

## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/confluentkafka)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/confluentkafka)

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
[Full Example Source](/plugin/confluentkafka/example/consumer/consumer.go)

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

With a delivery channel, the span event ends when the delivery report arrives, so a failed delivery is recorded on it, and the report is then forwarded to the channel.
Without one, the delivery report goes to `Events()` as usual and the span event covers only the enqueue.

[Full Example Source](/plugin/confluentkafka/example/producer/producer.go)
