package main

import (
	"fmt"
	memtracer "github.com/ONG-YA/gomemcache"
	"log"
	"net/http"
	"os"

	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

var memcacheClient *memtracer.Client

func doMemcache(w http.ResponseWriter, r *http.Request) {
	item, err := memcacheClient.Get(r.Context(), "key")
	if err != nil {
		fmt.Println(err)
	}
	fmt.Printf("key:%s,value:%s", item.Key, string(item.Value))
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoMemcacheTest"),
		pinpoint.WithAgentId("GoMemcacheTestAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}

	addrs := []string{"localhost:22222", "localhost:33333"}

	memcacheClient = memtracer.NewClient(addrs...)

	http.HandleFunc(phttp.WrapHandleFunc(agent, "memcacheTest", "/memcache", doMemcache))

	http.ListenAndServe(":9000", nil)

	agent.Shutdown()
}
