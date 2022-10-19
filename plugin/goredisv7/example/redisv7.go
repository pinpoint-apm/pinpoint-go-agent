package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-redis/redis/v7"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv7"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

var redisClient *redis.Client

func redisv7(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	client := redisClient.WithContext(ctx)

	pipe := client.Pipeline()
	incr := pipe.Incr("foo")
	pipe.Expire("foo", time.Hour)
	_, er := pipe.Exec()
	fmt.Println(incr.Val(), er)

	err := client.Set("key", "value", 0).Err()
	if err != nil {
		fmt.Println(err)
	}

	val, err := client.Get("key").Result()
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println("key", val)
}

var redisClusterClient *redis.ClusterClient

func redisv7Cluster(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	client := redisClusterClient.WithContext(ctx)

	pipe := client.Pipeline()
	incr := pipe.Incr("foo")
	pipe.Expire("foo", time.Hour)
	_, er := pipe.Exec()
	fmt.Println(incr.Val(), er)

	err := client.Set("key", "value", 0).Err()
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(500)
		return
	}

	val, err := client.Get("key").Result()
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(500)
		return
	}
	fmt.Println("key", val)
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoRedisv7Test"),
		pinpoint.WithAgentId("GoRedisv7TestAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	addrs := []string{"localhost:6379", "localhost:6380"}

	//redis client
	redisOpts := &redis.Options{
		Addr: addrs[0],
	}
	redisClient = redis.NewClient(redisOpts)
	redisClient.AddHook(ppgoredisv7.NewHook(redisOpts))

	//redis cluster client
	redisClusterOpts := &redis.ClusterOptions{
		Addrs: addrs,
	}
	redisClusterClient = redis.NewClusterClient(redisClusterOpts)
	redisClusterClient.AddHook(ppgoredisv7.NewClusterHook(redisClusterOpts))

	http.HandleFunc("/redis", pphttp.WrapHandlerFunc(redisv7))
	http.HandleFunc("/rediscluster", pphttp.WrapHandlerFunc(redisv7Cluster))

	http.ListenAndServe(":9000", nil)

	redisClient.Close()
	redisClusterClient.Close()
}
