package main

import (
	"context"
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
	ctx := pinpoint.NewContext(context.Background(), pinpoint.FromContext(r.Context()))
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://localhost:9000/hello", nil)

	resp, err := phttp.DoClient(http.DefaultClient.Do, "http.DefaultClient.Do", req)
	if nil != err {
		io.WriteString(w, err.Error())
		return
	}
	defer resp.Body.Close()
	io.Copy(w, resp.Body)
}

func wrapClient(w http.ResponseWriter, r *http.Request) {
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
		pinpoint.WithAppName("GoHttpTest"),
		pinpoint.WithAgentId("GoHttpAgent"),
		pinpoint.WithHttpStatusCodeError([]string{"5xx", "4xx"}),
		pinpoint.WithHttpExcludeUrl([]string{"/wrapreq*", "/**/*.go", "/*/*.do", "/abc**"}),
		pinpoint.WithHttpExcludeMethod([]string{"put", "POST"}),
		//pinpoint.WithHttpRecordRequestHeader([]string{"HEADERS-ALL"}),
		pinpoint.WithHttpRecordRequestHeader([]string{"user-agent", "connection", "foo"}),
		//pinpoint.WithHttpRecordRespondHeader([]string{"content-length"}),
		pinpoint.WithHttpRecordRespondHeader([]string{"HEADERS-ALL"}),
		pinpoint.WithHttpRecordRequestCookie([]string{"_octo"}),
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
