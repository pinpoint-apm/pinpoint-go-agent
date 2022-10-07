package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/redigo"
)

func redigo_test(w http.ResponseWriter, r *http.Request) {
	//Dial
	c, err := ppredigo.Dial("tcp", "127.0.0.1:6379")
	if err != nil {
		log.Fatal(err)
	}

	c.Do("SET", "vehicle", "truck") //not traced

	tracer := pinpoint.FromContext(r.Context())
	ctx := pinpoint.NewContext(context.Background(), tracer)
	ppredigo.WithContext(c, ctx)

	c.Do("SET", "vehicle", "truck")
	redis.DoWithTimeout(c, 1000*time.Millisecond, "GET", "vehicle")

	ppredigo.WithContext(c, context.Background())
	c.Do("SET", "vehicle", "bus") //not traced

	redis.DoContext(c, ctx, "GET", "vehicle")
	redis.DoContext(c, context.Background(), "EXISTS", "vehicle")
	c.Close()

	//DialUrl
	c, err = ppredigo.DialURL("redis://127.0.0.1:6379")
	if err != nil {
		log.Fatal(err)
	}

	ppredigo.WithContext(c, ctx)
	c.Do("SET", "vehicle", "suv")
	redis.DoWithTimeout(c, 1000*time.Millisecond, "GET", "vehicle")
	redis.DoContext(c, ctx, "INCR", "foo")
	c.Close()
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoRedigoTest"),
		pinpoint.WithAgentId("GoRedigoTestAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	http.HandleFunc("/redis", pphttp.WrapHandlerFunc(redigo_test))
	http.ListenAndServe(":9000", nil)
}
