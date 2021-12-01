package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"time"

	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

type handler struct {
	agent pinpoint.Agent
}

func doRequest(tracer pinpoint.Tracer) (string, error) {
	req, err := http.NewRequest("GET", "http://localhost:9000/hello", nil)
	if nil != err {
		return "", err
	}
	client := &http.Client{}
	tracer = phttp.NewHttpClientTracer(tracer, "doRequest", req)
	resp, err := client.Do(req)
	phttp.EndHttpClientTracer(tracer, resp, err)

	if nil != err {
		return "", err
	}

	fmt.Println("response code is", resp.StatusCode)

	ret, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	func() {
		defer tracer.NewSpanEvent("f1").EndSpanEvent()

		func() {
			defer tracer.NewSpanEvent("f2").EndSpanEvent()
			time.Sleep(2 * time.Millisecond)
		}()
		time.Sleep(2 * time.Millisecond)
	}()

	return string(ret), nil
}

func (h *handler) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	tracer := phttp.NewHttpServerTracer(h.agent, req, "Go Http Server")
	defer tracer.EndSpan()

	apiId := h.agent.RegisterSpanApiId("Go Http Server", pinpoint.ApiTypeWebRequest)
	tracer.Span().SetApiId(apiId)

	if req.URL.String() == "/hello" {
		ret, _ := doRequest(tracer)

		io.WriteString(writer, ret)
		time.Sleep(10 * time.Millisecond)
	} else {
		writer.WriteHeader(http.StatusNotFound)
	}

	phttp.TraceHttpStatus(tracer, http.StatusOK)
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("testGoAgent"),
		pinpoint.WithAgentId("testGoAgentId"),
		pinpoint.WithCollectorHost("localhost"),
	}
	c, _ := pinpoint.NewConfig(opts...)
	a, _ := pinpoint.NewAgent(c)

	server := http.Server{
		Addr:    ":8000",
		Handler: &handler{agent: a},
	}

	server.ListenAndServe()
	a.Shutdown()
}
