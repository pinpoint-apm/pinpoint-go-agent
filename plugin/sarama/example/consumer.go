package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"

	"github.com/Shopify/sarama"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	psarama "github.com/pinpoint-apm/pinpoint-go-agent/plugin/sarama"
)

const ctopic = "go-sarama-test"

var cbrokers = []string{"127.0.0.1:9092"}

func callElastic(tracer pinpoint.Tracer) (string, error) {
	req, err := http.NewRequest("GET", "http://localhost:9000/goelastic", nil)
	if nil != err {
		return "", err
	}

	client := &http.Client{}
	tracer = phttp.NewHttpClientTracer(tracer, "callElastic", req)
	resp, err := client.Do(req)
	phttp.EndHttpClientTracer(tracer, resp, err)

	if nil != err {
		return "", err
	}

	fmt.Println("response code is", resp.StatusCode)

	ret, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	return string(ret), nil
}

func processMessage(msg *psarama.ConsumerMessage) {
	tracer := msg.Tracer()
	defer tracer.EndSpan()
	defer tracer.NewSpanEvent("processMessage").EndSpanEvent()

	fmt.Println("Retrieving message: ", string(msg.Value))

	ret, _ := callElastic(tracer)
	fmt.Println("call Elasticsearch: ", ret)
}

func subscribe(topic string, consumer sarama.Consumer, agent pinpoint.Agent) {
	partitionList, err := consumer.Partitions(topic) //get all partitions on the given topic
	if err != nil {
		fmt.Println("Error retrieving partitionList ", err)
	}
	initialOffset := sarama.OffsetOldest //get offset for the oldest message on the topic

	for _, partition := range partitionList {
		pc, _ := consumer.ConsumePartition(topic, partition, initialOffset)

		go func(pc sarama.PartitionConsumer) {
			for msg := range pc.Messages() {
				processMessage(psarama.WrapConsumerMessage(msg, agent))
			}
		}(pc)
	}
}

func index(w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, "hello world")
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

	http.HandleFunc(phttp.WrapHandleFunc(agent, "index", "/", index))
	log.Fatal(http.ListenAndServe(":8082", nil))
}
