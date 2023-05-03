package main

import (
	"fmt"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	pphttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/rueidis"
	"github.com/redis/rueidis"
	"github.com/redis/rueidis/rueidishook"
	"net/http"

	"log"
	"os"
)

var client rueidis.Client

func rueidisv1(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	client = client

	err := client.Do(ctx, client.B().Set().Key("foo").Value("bar").Nx().Build()).Error()
	if err != nil {
		fmt.Println(err)
	}

	val, err := client.Do(ctx, client.B().Get().Key("foo").Build()).ToString()
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println("foo", val)

}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoRueidisTest"),
		pinpoint.WithAgentId("GoRueidisTestAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)

	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	addrs := []string{"localhost:6379", "localhost:6380"}

	rueidisClientOpts := rueidis.ClientOption{
		InitAddress: addrs,
	}

	client, err = rueidis.NewClient(rueidisClientOpts)
	if err != nil {
		return
	}
	if err != nil {
		panic(err)
	}
	client = rueidishook.WithHook(client, pprueidis.NewHook(rueidisClientOpts))

	http.HandleFunc("/rueidis", pphttp.WrapHandlerFunc(rueidisv1))

	http.ListenAndServe(":9000", nil)

	client.Close()
}