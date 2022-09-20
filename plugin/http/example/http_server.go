package main

import (
	"context"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"io"
	"log"
	"net/http"
	"os"
)

func index(w http.ResponseWriter, r *http.Request) {
	tracer := pinpoint.FromContext(r.Context())
	defer tracer.NewSpanEvent("dummy").EndSpanEvent()

	io.WriteString(w, "hello world")
}

func wrapRequest(w http.ResponseWriter, r *http.Request) {
	ctx := pinpoint.NewContext(context.Background(), pinpoint.FromContext(r.Context()))
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://localhost:9000/hello", nil)

	resp, err := phttp.DoClient(http.DefaultClient.Do, req)
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
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),

		phttp.WithHttpServerStatusCodeError([]string{"5xx", "4xx"}),
		phttp.WithHttpServerExcludeUrl([]string{"/wrapreq*", "/**/*.go", "/*/*.do", "/abc**"}),
		phttp.WithHttpServerExcludeMethod([]string{"put", "POST"}),
		phttp.WithHttpServerRecordRequestHeader([]string{"HEADERS-ALL"}),
		phttp.WithHttpClientRecordRequestHeader([]string{"user-agent", "connection", "foo"}),
		phttp.WithHttpServerRecordRespondHeader([]string{"content-length"}),
		phttp.WithHttpClientRecordRespondHeader([]string{"HEADERS-ALL"}),
		phttp.WithHttpServerRecordRequestCookie([]string{"_octo"}),
		phttp.WithHttpClientRecordRequestCookie([]string{"HEADERS-ALL"}),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	http.HandleFunc(phttp.WrapHandleFunc("/", index))
	http.HandleFunc(phttp.WrapHandleFunc("/wraprequest", wrapRequest))
	http.HandleFunc(phttp.WrapHandleFunc("/wraprequest/a.zo", wrapRequest))
	http.HandleFunc(phttp.WrapHandleFunc("/wraprequest/aa/b.zo", wrapRequest))
	http.HandleFunc(phttp.WrapHandleFunc("/wrapclient", wrapClient))
	http.HandleFunc(phttp.WrapHandleFunc("/wrapclient/aa/a.go", wrapClient))
	http.HandleFunc(phttp.WrapHandleFunc("/wrapclient/aa/bb/a.go", wrapClient))
	http.HandleFunc(phttp.WrapHandleFunc("/wrapclient/c.do", wrapClient))
	http.HandleFunc(phttp.WrapHandleFunc("/wrapclient/dd/d.do", wrapClient))
	http.HandleFunc(phttp.WrapHandleFunc("/wrapclient/c@do", wrapClient))
	http.HandleFunc(phttp.WrapHandleFunc("/abcd", wrapClient))
	http.HandleFunc(phttp.WrapHandleFunc("/abcd/e.go", wrapClient))

	http.ListenAndServe(":8000", nil)
}
