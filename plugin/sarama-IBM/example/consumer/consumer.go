package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/IBM/sarama"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama-IBM"
)

func outGoingRequest(ctx context.Context) string {
	client := pphttp.WrapClient(nil)

	request, _ := http.NewRequest("GET", "http://localhost:9001/query", nil)
	request = request.WithContext(ctx)

	resp, err := client.Do(request)
	if nil != err {
		return err.Error()
	}
	defer resp.Body.Close()

	ret, _ := ioutil.ReadAll(resp.Body)
	return string(ret)
}

func processMessage(ctx context.Context, msg *sarama.ConsumerMessage) error {
	tracer := pinpoint.FromContext(ctx)
	defer tracer.NewSpanEvent("processMessage").EndSpanEvent()

	fmt.Printf("Message topic:%q partition:%d offset:%d\n", msg.Topic, msg.Partition, msg.Offset)
	fmt.Println("retrieving message: ", string(msg.Value))

	ret := outGoingRequest(pinpoint.NewContext(context.Background(), tracer))
	fmt.Println("outGoingRequest: ", ret)

	return nil
}

func subscribe() {
	broker := []string{"localhost:9092"}
	config := sarama.NewConfig()
	config.Version = sarama.V2_3_0_0
	consumer, err := sarama.NewConsumer(broker, config)
	if err != nil {
		log.Fatalf("Could not create consumer: %v", err)
	}

	topic := "go-sarama-test"
	partitionList, err := consumer.Partitions(topic)
	if err != nil {
		fmt.Println("Error retrieving partitionList ", err)
	}
	initialOffset := sarama.OffsetNewest

	var wg sync.WaitGroup
	ctx := ppsaramaibm.NewContext(context.Background(), broker)

	for _, partition := range partitionList {
		pc, _ := consumer.ConsumePartition(topic, partition, initialOffset)

		go func(pc sarama.PartitionConsumer) {
			for msg := range pc.Messages() {
				ppsaramaibm.ConsumeMessageContext(processMessage, ctx, msg)
			}
			wg.Done()
		}(pc)
		wg.Add(1)
	}
	wg.Wait()
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoKafkaConsumer"),
		pinpoint.WithAgentId("GoKafkaConsumerAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	subscribe()
}
