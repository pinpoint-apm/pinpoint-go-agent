package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-redis/redis/v8"
	ppgoredisv8 "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv8/v2"
	pphttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

var redisClient *redis.Client

func redisv8(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	client := redisClient

	pipe := client.Pipeline()
	incr := pipe.Incr(ctx, "foo")
	pipe.Expire(ctx, "foo", time.Hour)
	_, er := pipe.Exec(ctx)
	fmt.Println(incr.Val(), er)

	err := client.Set(ctx, "key", "value", 0).Err()
	if err != nil {
		fmt.Println(err)
	}

	val, err := client.Get(ctx, "key").Result()
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println("key", val)
}

var redisClusterClient *redis.ClusterClient

func redisv8Cluster(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	client := redisClusterClient

	pipe := client.Pipeline()
	incr := pipe.Incr(ctx, "foo")
	pipe.Expire(ctx, "foo", time.Hour)
	_, er := pipe.Exec(ctx)
	fmt.Println(incr.Val(), er)

	err := client.Set(ctx, "key", "value", 0).Err()
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(500)
		return
	}

	val, err := client.Get(ctx, "key").Result()
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(500)
		return
	}
	fmt.Println("key", val)
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoRedisv8Test"),
		pinpoint.WithAgentName("GoRedisv8TestAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	//redis client
	redisOpts := &redis.Options{
		Addr: "localhost:6379",
	}
	redisClient = redis.NewClient(redisOpts)
	redisClient.AddHook(ppgoredisv8.NewHook(redisOpts))

	//redis cluster client
	redisClusterOpts := &redis.ClusterOptions{
		Addrs: []string{"localhost:6380"},
	}
	redisClusterClient = redis.NewClusterClient(redisClusterOpts)
	redisClusterClient.AddHook(ppgoredisv8.NewClusterHook(redisClusterOpts))

	http.HandleFunc("/redis", pphttp.WrapHandlerFunc(redisv8))
	http.HandleFunc("/rediscluster", pphttp.WrapHandlerFunc(redisv8Cluster))

	http.ListenAndServe(":9011", nil)

	redisClient.Close()
	redisClusterClient.Close()
}
