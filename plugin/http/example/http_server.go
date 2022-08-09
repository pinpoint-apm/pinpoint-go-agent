package main

import (
	"io"
	"log"
	"net/http"
	"os"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func index(w http.ResponseWriter, r *http.Request) {
	tracer := pinpoint.FromContext(r.Context())
	defer tracer.NewSpanEvent("dummy").EndSpanEvent()

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
		pinpoint.WithHttpStatusCodeError([]string{"5xx", "4xx"}),
		pinpoint.WithHttpExcludeUrl([]string{"/wrapreq*", "/**/*.go", "/*/*.do", "/abc**"}),
		pinpoint.WithHttpExcludeMethod([]string{"put", "POST"}),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}

	http.HandleFunc(phttp.WrapHandleFunc(agent, "index", "/", index))
	http.HandleFunc(phttp.WrapHandleFunc(agent, "wrapRequest", "/wraprequest", wrapRequest))
	http.HandleFunc(phttp.WrapHandleFunc(agent, "wrapRequest2", "/wraprequest/a.zo", wrapRequest))
	http.HandleFunc(phttp.WrapHandleFunc(agent, "wrapRequest3", "/wraprequest/aa/b.zo", wrapRequest))
	http.HandleFunc(phttp.WrapHandleFunc(agent, "wrapClient", "/wrapclient", wrapClient))
	http.HandleFunc(phttp.WrapHandleFunc(agent, "wrapClient2", "/wrapclient/aa/a.go", wrapClient))
	http.HandleFunc(phttp.WrapHandleFunc(agent, "wrapClient3", "/wrapclient/aa/bb/a.go", wrapClient))
	http.HandleFunc(phttp.WrapHandleFunc(agent, "wrapClient4", "/wrapclient/c.do", wrapClient))
	http.HandleFunc(phttp.WrapHandleFunc(agent, "wrapClient5", "/wrapclient/dd/d.do", wrapClient))
	http.HandleFunc(phttp.WrapHandleFunc(agent, "wrapClient6", "/wrapclient/c@do", wrapClient))
	http.HandleFunc(phttp.WrapHandleFunc(agent, "wrapClient7", "/abcd", wrapClient))
	http.HandleFunc(phttp.WrapHandleFunc(agent, "wrapClient8", "/abcd/e.go", wrapClient))

	http.ListenAndServe(":8000", nil)
	agent.Shutdown()
}
