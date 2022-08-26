package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/Shopify/sarama"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	psarama "github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama"
)

const ctopic = "go-sarama-test"

var cbrokers = []string{"127.0.0.1:9092"}

func outGoingRequest(ctx context.Context) string {
	client := phttp.WrapClient(nil)

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

func processMessage(msg *psarama.ConsumerMessage) error {
	tracer := msg.Tracer()
	defer tracer.NewSpanEvent("processMessage").EndSpanEvent()

	fmt.Printf("Message topic:%q partition:%d offset:%d\n", msg.Topic, msg.Partition, msg.Offset)
	fmt.Println("retrieving message: ", string(msg.Value))

	ret := outGoingRequest(pinpoint.NewContext(context.Background(), tracer))
	fmt.Println("outGoingRequest: ", ret)

	return nil
}

func subscribe(topic string, consumer sarama.Consumer, agent pinpoint.Agent) {
	partitionList, err := consumer.Partitions(topic) //get all partitions on the given topic
	if err != nil {
		fmt.Println("Error retrieving partitionList ", err)
	}
	initialOffset := sarama.OffsetOldest //get offset for the oldest message on the topic

	var wg sync.WaitGroup

	for _, partition := range partitionList {
		pc, _ := consumer.ConsumePartition(topic, partition, initialOffset)

		go func(pc sarama.PartitionConsumer) {
			for msg := range pc.Messages() {
				psarama.ConsumeMessage(processMessage, msg, agent)
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

	config := sarama.NewConfig()
	config.Version = sarama.V2_3_0_0
	consumer, err := sarama.NewConsumer(cbrokers, config)
	if err != nil {
		log.Fatalf("Could not create consumer: %v", err)
	}

	subscribe(ctopic, consumer, agent)
	agent.Shutdown()
}
