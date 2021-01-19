package main

import (
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func index(w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, "hello world")
}

func wrapRequest(w http.ResponseWriter, r *http.Request) {
	tracer := pinpoint.FromContext(r.Context())
	req, _ := http.NewRequest("GET", "http://localhost:9000/hello", nil)

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

func wrapClient(w http.ResponseWriter, r *http.Request) {
	client := &http.Client{}
	client = phttp.WrapClient(client)

	request, _ := http.NewRequest("GET", "http://localhost:9000/async", nil)
	request = request.WithContext(r.Context())

	resp, err := client.Do(request)
	if nil != err {
		io.WriteString(w, err.Error())
		return
	}
	defer resp.Body.Close()
	io.Copy(w, resp.Body)
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoHttpTest"),
		pinpoint.WithAgentId("GoHttpAgent"),
		pinpoint.WithCollectorHost("localhost"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}

	http.HandleFunc(phttp.WrapHandleFunc(agent, "index", "/", index))
	http.HandleFunc(phttp.WrapHandleFunc(agent, "wrapRequest", "/wraprequest", wrapRequest))
	http.HandleFunc(phttp.WrapHandleFunc(agent, "wrapClient", "/wrapclient", wrapClient))

	http.ListenAndServe(":9000", nil)
	agent.Shutdown()
}
