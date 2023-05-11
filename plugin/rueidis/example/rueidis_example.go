package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/rueidis"
	"github.com/redis/rueidis"
	"github.com/redis/rueidis/rueidishook"
)

func rueidisv1(w http.ResponseWriter, r *http.Request) {
	opt := rueidis.ClientOption{
		InitAddress: []string{"localhost:6379"},
	}

	client, err := rueidis.NewClient(opt)
	if err != nil {
		panic(err)
	}
	client = rueidishook.WithHook(client, pprueidis.NewHook(opt))

	ctx := r.Context()
	err = client.Do(ctx, client.B().Set().Key("foo").Value("bar").Nx().Build()).Error()
	if err != nil {
		fmt.Println(err)
	}

	val, err := client.Do(ctx, client.B().Get().Key("foo").Build()).ToString()
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println("foo", val)

	client.Close()
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

	http.HandleFunc("/rueidis", pphttp.WrapHandlerFunc(rueidisv1))
	http.ListenAndServe(":9000", nil)
}
