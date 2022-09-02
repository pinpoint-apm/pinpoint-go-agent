package main

import (
	"io"
	"log"
	"net/http"
	"os"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func indexMux(w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, "hello world, mux")
}

func outGoing(w http.ResponseWriter, r *http.Request) {
	client := phttp.WrapClientWithContext(r.Context(), &http.Client{})
	resp, err := client.Get("http://localhost:9000/async_wrapper?foo=bar&say=goodbye")

	if nil != err {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, err.Error())
		return
	}
	defer resp.Body.Close()

	w.Header().Set("foo", "bar")
	w.WriteHeader(http.StatusAccepted)
	io.WriteString(w, "wrapClient success")
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoHttpMuxTest"),
		pinpoint.WithAgentId("GoHttpMuxAgent"),
		pinpoint.WithHttpStatusCodeError([]string{"5xx", "4xx"}),
		pinpoint.WithHttpRecordRequestHeader([]string{"user-agent", "connection", "foo"}),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}

	mux := phttp.NewServeMux(agent)
	mux.Handle("/foo", http.HandlerFunc(indexMux))
	mux.HandleFunc("/bar", outGoing)

	log.Fatal(http.ListenAndServe("localhost:8000", mux))

}
