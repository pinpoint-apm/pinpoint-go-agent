package main

//Contributed by ONG-YA (https://github.com/ONG-YA)

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/gomemcache"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func doMemcache(w http.ResponseWriter, r *http.Request) {
	addr := []string{"localhost:11211"}

	mc := ppgomemcache.NewClient(addr...)
	mc.WithContext(r.Context())

	_, _ = mc.Get("foo") // error

	err := mc.Set(&memcache.Item{Key: "foo", Value: []byte("foo value")})
	if err != nil {
		fmt.Println(err)
	}

	item, err := mc.Get("foo")
	if err != nil {
		fmt.Println(err)
	}
	fmt.Printf("key: %s, value: %s", item.Key, string(item.Value))

	bar := &memcache.Item{Key: "bar", Value: []byte("bar value")}
	err = mc.Add(bar)
	if err != nil {
		fmt.Println(err)
	}

	barr := &memcache.Item{Key: "bar", Value: []byte("bar value replace")}
	err = mc.Replace(barr)
	if err != nil {
		fmt.Println(err)
	}

	m, err := mc.GetMulti([]string{"foo", "bar"})
	fmt.Printf("key: %s, value: %s", m["foo"].Key, string(m["foo"].Value))
	fmt.Printf("key: %s, value: %s", m["bar"].Key, string(m["bar"].Value))

	err = mc.Delete("foo")
	if err != nil {
		fmt.Println(err)
	}

	err = mc.DeleteAll()
	if err != nil {
		fmt.Println(err)
	}

	err = mc.Ping()
	if err != nil {
		fmt.Println(err)
	}
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
	defer agent.Shutdown()

	http.HandleFunc("/memcache", pphttp.WrapHandlerFunc(doMemcache))

	http.ListenAndServe(":9000", nil)
}
