# ppsaramaibm
This package instruments the [IBM/sarama](https://github.com/IBM/sarama) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama-IBM/v2
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama-IBM/v2"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama-IBM/v2)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama-IBM/v2)

This package instruments Kafka consumers and producers.

### Consumer
To instrument a Kafka consumer, use ConsumeMessageContext.
To show the broker the consumer reads from on the pinpoint screen, pass it a context with the broker addresses, made by NewContext.
Without one, the span shows the broker the producer recorded on the message, or `Unknown` for a message from an uninstrumented producer.

ConsumePartition example:
``` go
ctx := ppsaramaibm.NewContext(context.Background(), broker)
pc, _ := consumer.ConsumePartition(topic, partition, offset)
for msg := range pc.Messages() {
    ppsaramaibm.ConsumeMessageContext(processMessage, ctx, msg)
}
```
ConsumerGroupHandler example:
``` go
func (h exampleConsumerGroupHandler) ConsumeClaim(sess sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
    ctx := sess.Context()
    for msg := range claim.Messages() {
        _ = ppsaramaibm.ConsumeMessageContext(process, ctx, msg)
        sess.MarkMessage(msg, "")
    }
    return nil
}

func main() {     
    ctx := ppsaramaibm.NewContext(context.Background(), broker)
    handler := exampleConsumerGroupHandler{}
    err := group.Consume(ctx, topics, handler)
```

ConsumeMessageContext records the handler call as a span event named after the handler function,
as the HTTP and gRPC server plugins do, and passes a context added pinpoint.Tracer to HandlerContextFunc.
In HandlerContextFunc, this tracer can be obtained by using the pinpoint.FromContext function.
Alternatively, the context may be propagated where the context that contains the pinpoint.Tracer is required:
calls traced with it are recorded under the handler's event.

``` go
func process(ctx context.Context, msg *sarama.ConsumerMessage) error {
    fmt.Printf("Message topic:%q partition:%d offset:%d\n", msg.Topic, msg.Partition, msg.Offset)
```

The span ends when the handler returns, so call ConsumeMessageContext on the goroutine that processes the message;
in a worker pool, call it in the worker. See [Message queues](/doc/plugin_guide.md#message-queues).

``` go
package main

import (
    "context"
    "fmt"

    "github.com/IBM/sarama"
    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama-IBM/v2"
)

func processMessage(ctx context.Context, msg *sarama.ConsumerMessage) error {
    fmt.Println("retrieving message: ", string(msg.Value))
    ...
}

func subscribe() {
    broker := []string{"localhost:9092"}
    config := sarama.NewConfig()
    consumer, err := sarama.NewConsumer(broker, config)
    ...
    
    ctx := ppsaramaibm.NewContext(context.Background(), broker)
    for _, partition := range partitionList {
        pc, _ := consumer.ConsumePartition(topic, partition, initialOffset)

        go func(pc sarama.PartitionConsumer) {
            for msg := range pc.Messages() {
                ppsaramaibm.ConsumeMessageContext(processMessage, ctx, msg)
            }
        }(pc)
    }
    ...
}

func main() {
    ... //setup agent

    subscribe()
}
```
[Full Example Source](/example/sarama-IBM/consumer/consumer.go)

### Producer
#### SyncProducer
To instrument a Kafka sync producer, use NewSyncProducer.

``` go
config := sarama.NewConfig()
config.Producer.Return.Successes = true
producer, err := ppsaramaibm.NewSyncProducer(brokers, config)
```

Use SendMessageContext, or SendMessagesContext for a batch, with the context containing the pinpoint.Tracer;
SendMessage and SendMessages produce without tracing.
A batch is recorded as one `sarama.SyncProducer.SendMessages` event, which carries the duration and the error,
with an event per message under it.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
partition, offset, err := producer.SendMessageContext(ctx, msg)
```

``` go
package main

import (
    "log"
    "net/http"

    "github.com/IBM/sarama"
    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama-IBM/v2"
)

func prepareMessage(topic, message string) *sarama.ProducerMessage {
    return &sarama.ProducerMessage{
        Topic:     topic,
        Value:     sarama.StringEncoder(message),
    }
}

func save(w http.ResponseWriter, r *http.Request) {
    msg := prepareMessage("topic", "Hello, Kafka!!")
    partition, offset, err := producer.SendMessageContext(r.Context(), msg)
    ...
}

var producer ppsaramaibm.SyncProducer

func main() {
    ... //setup agent
	
    config := sarama.NewConfig()
    config.Producer.Return.Successes = true
    ...

    var err error
    producer, err = ppsaramaibm.NewSyncProducer([]string{"127.0.0.1:9092"}, config)
    if err != nil {
        log.Fatal(err)
    }
    defer producer.Close()

    http.HandleFunc("/save", pphttp.WrapHandlerFunc(save))
}

```
[Full Example Source](/example/sarama-IBM/producer/producer.go)

#### AsyncProducer
To instrument a Kafka async producer, use NewAsyncProducer.

``` go
config := sarama.NewConfig()
config.Producer.Return.Successes = true
producer, err := ppsaramaibm.NewAsyncProducer(brokers, config)
```

Use InputContext with the context containing the pinpoint.Tracer; Input produces without tracing.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
producer.InputContext(ctx, msg)
```

InputContext records a `sarama.AsyncProducer.Input()` event on the caller, and under it a
`sarama.AsyncProducer.SendMessage` event that lasts until the broker acknowledges the message and records a failed delivery.
That needs both `config.Producer.Return.Successes` (off in sarama's default config) and `config.Producer.Return.Errors`
(on by default), and a `config.Version` of Kafka 0.11 or later; otherwise the event ends at once and a failed
delivery is not recorded.
An acknowledgment that arrives after the request has ended is still recorded on its event, but no longer fails the transaction.
Like Input, InputContext blocks until sarama takes the message; it does not watch the context's deadline.

Read `Errors()`, and `Successes()` when it is enabled, from the wrapper, as sarama requires of its own channels:
one nobody reads stalls the producer. Close the producer through the wrapper too.

``` go
package main

import (
    "log"
    "net/http"

    "github.com/IBM/sarama"
    "github.com/pinpoint-apm/pinpoint-go-agent/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama-IBM/v2"
)

func prepareAsyncMessage(topic, message string) *sarama.ProducerMessage {
    return &sarama.ProducerMessage{
        Topic:     topic,
        Value:     sarama.StringEncoder(message),
    }
}

func saveAsync(w http.ResponseWriter, r *http.Request) {
    msg := prepareAsyncMessage("topic", "Hello, Kafka!!")
    producer.InputContext(r.Context(), msg)
    ...
}

var producer ppsaramaibm.AsyncProducer

func main() {
    ... //setup agent
	
    config := sarama.NewConfig()
    config.Producer.Return.Successes = true
    ...

    var err error
    producer, err = ppsaramaibm.NewAsyncProducer([]string{"127.0.0.1:9092"}, config)
    if err != nil {
        log.Fatal(err)
    }
    defer producer.Close()
    go func() {
        for {
            select {
            case success := <-producer.Successes():
                log.Printf("Partition %d at offset %d\n", success.Partition, success.Offset)
            case err := <-producer.Errors():
                log.Printf("Failed to send message: %v", err)
            }
        }
    }()

    http.HandleFunc("/save", pphttp.WrapHandlerFunc(saveAsync))
}

```
[Full Example Source](/example/sarama-IBM/asyncproducer/asyncproducer.go)

#### Trace headers
Both producers pass the transaction on in record headers, which Kafka has from 0.11. With a `config.Version`
below `sarama.V0_11_0_0` (sarama's default is newer) neither producer writes a header, since sarama would reject
every message that carries one: the send is still recorded as a span event, but the consumer starts a new
transaction.

#### Producers created elsewhere
A producer created another way - `sarama.NewSyncProducerFromClient`, `sarama.NewAsyncProducerFromClient`,
or one a library hands you - is wrapped with WrapSyncProducer or WrapAsyncProducer.
`addrs[0]` is the broker shown on the pinpoint screen, and `config` is the producer's own configuration
(`nil` for sarama's default), which decides the header and acknowledgment handling above.

``` go
p, err := sarama.NewSyncProducerFromClient(client)
producer := ppsaramaibm.WrapSyncProducer(p, brokers, client.Config())
```

After WrapAsyncProducer, use only the wrapper: it reads the original producer's `Successes()` and `Errors()` itself
to end the message events, and its `Close()` drains them.
