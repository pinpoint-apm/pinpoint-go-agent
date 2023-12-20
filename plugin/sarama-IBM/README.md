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
To instrument a Kafka consumer, ConsumeMessageContext.
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
To instrument a Kafka producer, use NewSyncProducer or NewAsyncProducer.

``` go
config := sarama.NewConfig()
producer, err := ppsaramaibm.NewSyncProducer(brokers, config)
```

It is necessary to pass the context containing the pinpoint.Tracer to sarama.SyncProducer (or sarama.AsyncProducer) using WithContext function.

``` go
ppsaramaibm.WithContext(pinpoint.NewContext(context.Background(), tracer), producer)
partition, offset, err := producer.SendMessage(msg)
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
        Partition: -1,
        Value:     sarama.StringEncoder(message),
    }
}

func save(w http.ResponseWriter, r *http.Request) {
    msg := prepareMessage("topic", "Hello, Kafka!!")
    ppsaramaibm.WithContext(r.Context(), producer)
    partition, offset, err := producer.SendMessage(msg)
    ...
}

var producer sarama.SyncProducer

func main() {
    ... //setup agent
	
    config := sarama.NewConfig()
    ...

    producer, err := ppsaramaibm.NewSyncProducer([]string{"127.0.0.1:9092"}, config)
    http.HandleFunc("/save", pphttp.WrapHandlerFunc(save))
}

```
[Full Example Source](/plugin/sarama-IBM/example/producer.go)
