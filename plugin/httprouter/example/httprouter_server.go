package main

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/httprouter"
)

func Index(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	tracer := pinpoint.FromContext(r.Context())
	func() {
		defer tracer.NewSpanEvent("func_1").EndSpanEvent()

		func() {
			defer tracer.NewSpanEvent("func_2").EndSpanEvent()
		}()
		time.Sleep(1 * time.Second)
	}()

	fmt.Fprint(w, "Welcome!\n")
}

func Hello(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	sleep()
	fmt.Fprintf(w, "hello, %s!\n", ps.ByName("name"))
}

func blog(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	sleep()
	fmt.Fprintf(w, "blog: %s, %s\n", ps.ByName("category"), ps.ByName("post"))
}

func files(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	sleep()
	fmt.Fprintf(w, "files: %s\n", ps.ByName("filepath"))
}

func sleep() {
	seed := rand.NewSource(time.Now().UnixNano())
	random := rand.New(seed).Intn(10000)
	time.Sleep(time.Duration(random+1) * time.Millisecond)
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoHttpRouterTest"),
		pinpoint.WithHttpUrlStatEnable(true),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	//router := httprouter.New()
	//router.GET("/", pphttprouter.WrapHandle(Index))
	//router.GET("/hello/:name", Hello)

	router := pphttprouter.New()
	router.GET("/", Index)
	router.GET("/hello/:name", Hello)
	router.GET("/blog/:category/:post", blog)
	router.GET("/files/*filepath", files)

	router.HandlerFunc("GET", "/user/:name/age/:old", func(w http.ResponseWriter, r *http.Request) {
		sleep()
		ps := httprouter.ParamsFromContext(r.Context())
		fmt.Fprintf(w, "user: %s, %s\n", ps.ByName("name"), ps.ByName("old"))
	})

	log.Fatal(http.ListenAndServe(":8000", router))
}
