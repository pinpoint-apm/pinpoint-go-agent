package main

//Contributed by ONG-YA (https://github.com/ONG-YA)

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/bradfitz/gomemcache/memcache"
	ppgomemcache "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gomemcache/v2"
	pphttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

func doMemcache(w http.ResponseWriter, r *http.Request) {
	addr := []string{"localhost:11211"}

	mc := ppgomemcache.NewClient(addr...)
	mc = mc.WithContext(r.Context())

	_, _ = mc.Get("foo") // error

	err := mc.Set(&memcache.Item{Key: "foo", Value: []byte("foo value")})
	if err != nil {
		fmt.Println(err)
	}

	item, err := mc.Get("foo")
	if err != nil {
		// A miss or a memcached that is down leaves item nil.
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
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
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	// GetMulti leaves a missing key out of the map rather than erroring.
	for _, key := range []string{"foo", "bar"} {
		if it, ok := m[key]; ok {
			fmt.Printf("key: %s, value: %s", it.Key, string(it.Value))
		}
	}

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
		pinpoint.WithAgentName("GoMemcacheTestAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()
	defer pinpoint.ShutdownOnSignal(agent)() // SIGTERM, SIGINT

	http.HandleFunc("/memcache", pphttp.WrapHandlerFunc(doMemcache))

	http.ListenAndServe(":9017", nil)
}
