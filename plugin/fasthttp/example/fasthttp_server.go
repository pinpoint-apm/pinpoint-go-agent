package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"github.com/valyala/fasthttp"
)

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoFastHttpTest"),
		pinpoint.WithAgentId("GoFastHttpTestAgent"),
		pinpoint.WithHttpUrlStatEnable(true),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
		pphttp.WithHttpClientRecordRequestHeader([]string{"HEADERS-ALL"}),
		pphttp.WithHttpClientRecordRespondHeader([]string{"HEADERS-ALL"}),
		pphttp.WithHttpClientRecordRequestCookie([]string{"HEADERS-ALL"}),
		pphttp.WithHttpServerRecordRequestHeader([]string{"Cookie", "Accept"}),
		pphttp.WithHttpServerRecordRespondHeader([]string{"Set-Cookie", "X-My-Header"}),
		pphttp.WithHttpServerRecordRequestCookie([]string{"HEADERS-ALL"}),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	mux := func(ctx *fasthttp.RequestCtx) {
		path := string(ctx.Path())
		if strings.HasPrefix(path, "/foo") {
			ppfasthttp.WrapHandler(fooHandler, "/foo")(ctx)
		} else if strings.HasPrefix(path, "/bar") {
			ppfasthttp.WrapHandler(barHandler, "/bar")(ctx)
		} else {
			ctx.Error("not found", fasthttp.StatusNotFound)
		}
	}
	if err := fasthttp.ListenAndServe(":9000", mux); err != nil {
		log.Fatalf("Error in ListenAndServe: %v", err)
	}
}

func fooHandler(ctx *fasthttp.RequestCtx) {
	sleep()

	tracer := pinpoint.FromContext(ctx.UserValue(ppfasthttp.CtxKey).(context.Context))
	defer tracer.NewSpanEvent("f1").EndSpanEvent()
	defer tracer.NewSpanEvent("f2").EndSpanEvent()

	fmt.Fprintf(ctx, "Request method is %q\n", ctx.Method())
	fmt.Fprintf(ctx, "RequestURI is %q\n", ctx.RequestURI())
	fmt.Fprintf(ctx, "Requested path is %q\n", ctx.Path())

	client(ctx)

	ctx.SetContentType("text/plain; charset=utf8")
	ctx.Response.Header.Set("X-My-Header", "my-header-value")

	// Set cookies
	var c fasthttp.Cookie
	c.SetKey("cookie-name")
	c.SetValue("cookie-value")
	ctx.Response.Header.SetCookie(&c)
}

func barHandler(ctx *fasthttp.RequestCtx) {
	sleep()

	fmt.Fprintf(ctx, "RequestURI is %q\n", ctx.RequestURI())
	fmt.Fprintf(ctx, "Requested path is %q\n", ctx.Path())
	ctx.SetStatusCode(fasthttp.StatusBadRequest)
}

func client(ctx *fasthttp.RequestCtx) {
	// Get URI from a pool
	url := fasthttp.AcquireURI()
	url.Parse(nil, []byte("http://localhost:8080/"))
	url.SetUsername("Aladdin")
	url.SetPassword("Open Sesame")

	hc := &fasthttp.HostClient{
		Addr: "localhost:8080", // The host address and port must be set explicitly
	}

	req := fasthttp.AcquireRequest()
	req.SetURI(url)          // copy url into request
	fasthttp.ReleaseURI(url) // now you may release the URI

	req.Header.SetMethod(fasthttp.MethodGet)
	req.Header.SetCookie("cookie-name", "cookie-value")
	req.Header.Set("X-My-Header", "my-header-value")

	resp := fasthttp.AcquireResponse()

	ctxWithTracer := ctx.UserValue(ppfasthttp.CtxKey).(context.Context)
	err := ppfasthttp.DoClient(func() error {
		return hc.Do(req, resp)
	}, ctxWithTracer, req, resp)

	if err == nil {
		fmt.Printf("Response: %s\n", resp.Body())
	} else {
		fmt.Fprintf(os.Stderr, "Connection error: %v\n", err)
	}

	fasthttp.ReleaseRequest(req)
	fasthttp.ReleaseResponse(resp)
}

func sleep() {
	seed := rand.NewSource(time.Now().UnixNano())
	random := rand.New(seed).Intn(10000)
	time.Sleep(time.Duration(random+1) * time.Millisecond)
}
