package main

import (
	"fmt"
	"log"
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
	fmt.Fprintf(w, "hello, %s!\n", ps.ByName("name"))
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoHttpRouterTest"),
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

	log.Fatal(http.ListenAndServe(":8000", router))
}
