# ppsaramaibm
This package instruments the [IBM/sarama](https://github.com/IBM/sarama) package.

## Installation

```bash
$ go get github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama-IBM
```
```go
import "github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama-IBM"
```
## Usage
[![PkgGoDev](https://pkg.go.dev/badge/github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama-IBM)](https://pkg.go.dev/github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama-IBM)

This package instruments Kafka consumers and producers.

### Consumer
To instrument a Kafka consumer, use ConsumeMessageContext.
In order to display the kafka broker on the pinpoint screen, a context with broker addresses must be created and delivered using NewContext.

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
    }
}

func main() {     
    ctx := ppsaramaibm.NewContext(context.Background(), broker)
    handler := exampleConsumerGroupHandler{}
    err := group.Consume(ctx, topics, handler)
```

ConsumeMessageContext passes a context added pinpoint.Tracer to HandlerContextFunc.
In HandlerContextFunc, this tracer can be obtained by using the pinpoint.FromContext function.
Alternatively, the context may be propagated where the context that contains the pinpoint.Tracer is required.

``` go
func process(ctx context.Context, msg *sarama.ConsumerMessage) error {
    tracer := pinpoint.FromContext(ctx)
    defer tracer.NewSpanEvent("process").EndSpanEvent()

    fmt.Printf("Message topic:%q partition:%d offset:%d\n", msg.Topic, msg.Partition, msg.Offset)
```

``` go
package main

import (
    "github.com/IBM/sarama"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama-IBM"
)

func processMessage(ctx context.Context, msg *sarama.ConsumerMessage) error {
    tracer := pinpoint.FromContext(ctx)
    defer tracer.NewSpanEvent("processMessage").EndSpanEvent()
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
	...	
}

func main() {
    ... //setup agent

    subscribe()
}
```
[Full Example Source](/plugin/sarama-IBM/example/consumer.go)

### Producer
#### SyncProducer
To instrument a Kafka sync producer, use NewSyncProducer.

``` go
config := sarama.NewConfig()
config.Producer.Return.Successes = true
producer, err := ppsaramaibm.NewSyncProducer(brokers, config)
```

Use SendMessageContext with the context containing the pinpoint.Tracer.
You can also use SendMessage with WithContext, but we recommend using SendMessageContext because the WithContext is not thread-safe.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
partition, offset, err := producer.SendMessageContext(ctx, msg)
```

``` go
package main

import (
    "github.com/IBM/sarama"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama-IBM"
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

var producer sarama.SyncProducer

func main() {
    ... //setup agent
	
    config := sarama.NewConfig()
    config.Producer.Return.Successes = true
    ...

    producer, err := ppsaramaibm.NewSyncProducer([]string{"127.0.0.1:9092"}, config)
    http.HandleFunc("/save", pphttp.WrapHandlerFunc(save))
}

```
[Full Example Source](/plugin/sarama-IBM/example/producer.go)

#### AsyncProducer
To instrument a Kafka async producer, use NewAsyncProducer.

``` go
config := sarama.NewConfig()
config.Producer.Return.Successes = true
producer, err := ppsaramaibm.NewAsyncProducer(brokers, config)
```

Use InputContext with the context containing the pinpoint.Tracer.
You can also use Input with WithContext, but we recommend using InputContext because the WithContext is not thread-safe.

``` go
ctx := pinpoint.NewContext(context.Background(), tracer)
producer.InputContext(ctx, msg)
```

``` go
package main

import (
    "github.com/IBM/sarama"
    "github.com/pinpoint-apm/pinpoint-go-agent"
    "github.com/github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama-IBM"
)

func prepareAsyncMessage(topic, message string) *sarama.ProducerMessage {
    return &sarama.ProducerMessage{
        Topic:     topic,
        Value:     sarama.StringEncoder(message),
    }
}

func saveAsync(w http.ResponseWriter, r *http.Request) {
    msg := prepareMessage("topic", "Hello, Kafka!!")
    producer.InputContext(r.Context(), msg)
    ...
}

var producer sarama.AsyncProducer

func main() {
    ... //setup agent
	
    config := sarama.NewConfig()
    config.Producer.Return.Successes = true
    ...

    producer, err := ppsaramaibm.NewAsyncProducer([]string{"127.0.0.1:9092"}, config)
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
[Full Example Source](/plugin/sarama-IBM/example/asyncproducer.go)
