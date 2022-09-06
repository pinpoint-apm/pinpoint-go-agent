package main

import (
	"github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"io"
	"log"
	"net/http"
	"os"
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
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
		phttp.WithHttpStatusCodeError([]string{"5xx", "4xx"}),
		phttp.WithHttpRecordRequestHeader([]string{"user-agent", "connection", "foo"}),
	}

	//os.Setenv("PINPOINT_GO_USEPROFILE", "real")

	cfg, err := pinpoint.NewConfig(opts...)
	if err != nil {
		log.Fatalf("pinpoint configuration fail: %v", err)
	}

	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}

	mux := phttp.NewServeMux(agent)
	mux.Handle("/foo", http.HandlerFunc(indexMux))
	mux.HandleFunc("/bar", outGoing)

	log.Fatal(http.ListenAndServe("localhost:8000", mux))

}
