// Proxy app of the distributed tracing demo (see README.md).
//
// Shows client-side tracing and cross-process context propagation in one
// request flow, so the whole chain becomes a single distributed trace:
//
//	client → GoProxyExample → GoDbServerExample → MySQL
//
//	GET /api/members
//	  → WrapHandlerFunc opens the root span for the inbound request
//	    (remote address, endpoint, status code and the configured request
//	     headers - here User-Agent - are recorded by the wrapper)
//	  → the outbound call to the backend is traced as a child span event
//	     · WrapClient reads the tracer from the request context, sets
//	       ServiceTypeGoHttpClient, destination/endpoint and the URL
//	       annotation, and injects the Pinpoint-* propagation headers - the
//	       backend continues this trace from them
//	     · the downstream status code is recorded on the event
//	     · a failed call is recorded as an error on the event; recording it
//	       on the span too marks the whole transaction failed
//	  → the wrapper records the response and ends the span
package main

import (
	"io"
	"log"
	"net/http"
	"os"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	pphttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func members(w http.ResponseWriter, r *http.Request) {
	backend := envOr("BACKEND", "http://localhost:8081")

	// The request carries the tracer of the root span in its context, so the
	// wrapped client traces the call as a child span event of this request
	// and injects that event's trace context into the outbound headers.
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, backend+r.URL.Path, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "pinpoint-go-demo-proxy")

	resp, err := pphttp.WrapClient(nil).Do(req)
	if err != nil {
		// The client event already carries the error; recording it on the
		// span as well marks the transaction failed with its cause.
		pinpoint.FromContext(r.Context()).Span().SetError(err)
		http.Error(w, `{"error":"backend unreachable"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoProxyExample"),
		pinpoint.WithAgentId("GoProxyExampleAgent"),
		pinpoint.WithCollectorHost(envOr("PINPOINT_GO_COLLECTOR_HOST", "localhost")),
		// Record only the User-Agent request header on the span (see doc/config.md).
		pphttp.WithHttpServerRecordRequestHeader([]string{"User-Agent"}),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Printf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	addr := envOr("ADDR", ":8080")
	http.HandleFunc("/api/members", pphttp.WrapHandlerFunc(members, "Go Proxy"))

	log.Println("proxy listening on", addr, "forwarding to", envOr("BACKEND", "http://localhost:8081"))
	log.Fatal(http.ListenAndServe(addr, nil))
}
