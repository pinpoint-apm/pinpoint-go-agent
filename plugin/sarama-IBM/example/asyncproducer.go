package main

import (
	"context"
	"github.com/IBM/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama-IBM"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
)

var asyncProducer ppsaramaibm.AsyncProducer

func prepareAsyncMessage(topic, message string) *sarama.ProducerMessage {
	msg := &sarama.ProducerMessage{
		Topic: topic,
		Value: sarama.StringEncoder(message),
	}

	return msg
}

func saveAsync(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	ctx := r.Context()
	asyncProducer.InputContext(ctx, prepareAsyncMessage("go-sarama-test", "Hello, Sarama !!"))
	asyncProducer.InputContext(ctx, prepareAsyncMessage("go-kafka-test", "Hello, Kafka !!"))

	tracer := pinpoint.FromContext(ctx)
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func(asyncTracer pinpoint.Tracer) {
		defer wg.Done()
		defer asyncTracer.EndSpan() //!!must be called
		defer asyncTracer.NewSpanEvent("asyncProducerGoroutine").EndSpanEvent()

		ctx := pinpoint.NewContext(context.Background(), asyncTracer)
		asyncProducer.InputContext(ctx, prepareAsyncMessage("go-sarama-test", "Hello, Sarama Goroutine !!"))
	}(tracer.NewGoroutineTracer())
	wg.Wait()

	io.WriteString(w, "success")
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoKafkaAsyncProducer"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
		pinpoint.WithSamplingType("percent"),
		pinpoint.WithSamplingPercentRate(10),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	config := sarama.NewConfig()
	config.Producer.Partitioner = sarama.NewRandomPartitioner
	config.Producer.RequiredAcks = sarama.WaitForAll
	config.Producer.Return.Successes = true
	config.Version = sarama.V2_3_0_0

	var broker = []string{"localhost:9092"}
	asyncProducer, err = ppsaramaibm.NewAsyncProducer(broker, config)
	if err != nil {
		log.Fatalf("Could not create producer: %v ", err)
	}
	defer asyncProducer.Close()

	go func() {
		for {
			select {
			case success := <-asyncProducer.Successes():
				log.Printf("Message sent to partition %d at offset %d\n", success.Partition, success.Offset)
			case err := <-asyncProducer.Errors():
				log.Printf("Failed to send message: %v", err)
			}
		}
	}()

	http.HandleFunc("/save_async", pphttp.WrapHandlerFunc(saveAsync))
	log.Fatal(http.ListenAndServe(":8081", nil))
}
