package main

import (
	"io"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"

	"github.com/go-chi/chi"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	pchi "github.com/pinpoint-apm/pinpoint-go-agent/plugin/chi"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

var agent *pinpoint.Agent

func hello(w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, "hello world")
}

func shutdown(w http.ResponseWriter, r *http.Request) {
	agent.Shutdown()
	io.WriteString(w, "shutdown")
}

func outgoing(w http.ResponseWriter, r *http.Request) {
	tracer := pinpoint.FromContext(r.Context())
	req, _ := http.NewRequest("GET", "http://localhost:9000/query", nil)

	tracer = phttp.NewHttpClientTracer(tracer, "http.DefaultClient.Do", req)
	resp, err := http.DefaultClient.Do(req)
	phttp.EndHttpClientTracer(tracer, resp, err)

	if nil != err {
		io.WriteString(w, err.Error())
		return
	}
	defer resp.Body.Close()
	io.Copy(w, resp.Body)
}

func main() {
	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()

	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoChiTest"),
		pinpoint.WithAgentId("GoChiTestAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
		//pinpoint.WithSamplingRate(5),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, _ = pinpoint.NewAgent(cfg)
	//if err != nil {
	//	log.Fatalf("pinpoint agent start fail: %v", err)
	//}

	r := chi.NewRouter()
	r.Use(pchi.Middleware(agent))
	r.Get("/hello", hello)
	r.Get("/outgoing", outgoing)
	r.Get("/shutdown", shutdown)

	http.ListenAndServe(":8000", r)
	agent.Shutdown()
}
