package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttprouter"
	"github.com/valyala/fasthttp"
)

func user(ctx *fasthttp.RequestCtx) {
	sleep()

	tracer := pinpoint.FromContext(ctx.UserValue(ppfasthttp.CtxKey).(context.Context))
	defer tracer.NewSpanEvent("f1").EndSpanEvent()
	defer tracer.NewSpanEvent("f2").EndSpanEvent()

	fmt.Fprintf(ctx, "hello, %s!\n", ctx.UserValue("name"))
}

func users(ctx *fasthttp.RequestCtx) {
	sleep()
	fmt.Fprintf(ctx, "hi, %s, %s!\n", ctx.UserValue("name1"), ctx.UserValue("name2"))
}

func ping(ctx *fasthttp.RequestCtx) {
	sleep()
	p := ctx.QueryArgs().Peek("pong")
	fmt.Fprintf(ctx, "Pong! %s\n", string(p))
}

func error(ctx *fasthttp.RequestCtx) {
	sleep()
	ctx.Error("NotImplemented", fasthttp.StatusNotImplemented)
}

func sleep() {
	seed := rand.NewSource(time.Now().UnixNano())
	random := rand.New(seed).Intn(10000)
	time.Sleep(time.Duration(random+1) * time.Millisecond)
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoFasthttpRouterTest"),
		pinpoint.WithHttpUrlStatEnable(true),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	r := ppfasthttprouter.New()
	r.GET("/user/{name}", user)
	r.GET("/users/{name1}/{name2}", users)
	r.GET("/ping", ping)
	r.GET("/error", error)

	log.Fatal(fasthttp.ListenAndServe(":9000", r.Handler))
}
