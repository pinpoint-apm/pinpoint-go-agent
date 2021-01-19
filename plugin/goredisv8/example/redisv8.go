package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-redis/redis/v8"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	predis "github.com/pinpoint-apm/pinpoint-go-agent/plugin/goredisv8"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

var redisClient *redis.Client

func redisv8(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	client := redisClient.WithContext(ctx)

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
	client := redisClusterClient.WithContext(ctx)

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

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoRedisv8Test"),
		pinpoint.WithAgentId("GoRedisv8TestAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}

	addrs := []string{"localhost:6379", "localhost:6380"}

	//redis client
	redisOpts := &redis.Options{
		Addr: addrs[0],
	}
	redisClient = redis.NewClient(redisOpts)
	redisClient.AddHook(predis.NewHook(redisOpts))

	//redis cluster client
	redisClusterOpts := &redis.ClusterOptions{
		Addrs: addrs,
	}
	redisClusterClient = redis.NewClusterClient(redisClusterOpts)
	redisClusterClient.AddHook(predis.NewClusterHook(redisClusterOpts))

	http.HandleFunc(phttp.WrapHandleFunc(agent, "redisTest", "/redis", redisv8))
	http.HandleFunc(phttp.WrapHandleFunc(agent, "redisClusterTest", "/rediscluster", redisv8Cluster))

	http.ListenAndServe(":9000", nil)

	redisClient.Close()
	redisClusterClient.Close()
	agent.Shutdown()
}
