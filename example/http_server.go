package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	_ "github.com/pinpoint-apm/pinpoint-go-agent/plugin/mysql"
)

func index(w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, "hello world")
}

func seterror(w http.ResponseWriter, r *http.Request) {
	tracer := pinpoint.FromContext(r.Context())
	tracer.SpanEvent().SetError(errors.New("my error message"))
	w.WriteHeader(500)
}

func httpClientRequest(w http.ResponseWriter, ctx context.Context) {
	client := phttp.WrapClient(nil)

	request, _ := http.NewRequest("GET", "http://localhost:9000/hello", nil)
	request = request.WithContext(ctx)

	resp, err := client.Do(request)
	if nil != err {
		io.WriteString(w, err.Error())
		return
	}
	defer resp.Body.Close()
	io.Copy(w, resp.Body)
}

func outgoing(w http.ResponseWriter, r *http.Request) {
	httpClientRequest(w, r.Context())
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoExample"),
		pinpoint.WithAgentId("GoExampleAgent"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
		//pinpoint.WithConfigFile("tmp/pinpoint-config.yaml"),
	}
	c, _ := pinpoint.NewConfig(opts...)
	agent, _ := pinpoint.NewAgent(c)
	defer agent.Shutdown()

	http.HandleFunc(phttp.WrapHandleFunc("/", index))
	http.HandleFunc(phttp.WrapHandleFunc("/error", seterror))
	http.HandleFunc(phttp.WrapHandleFunc("/outgoing", outgoing))

	http.ListenAndServe(":9000", nil)
}
