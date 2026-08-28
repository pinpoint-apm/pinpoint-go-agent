package main

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	pphttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"github.com/pinpoint-apm/pinpoint-go-agent/test/e2e/internal/e2e"
)

var httpClient = &http.Client{Timeout: 10 * time.Second}

type downstreamBody struct {
	TraceID              string `json:"trace_id"`
	Sampled              bool   `json:"sampled"`
	IncomingParentSpanID string `json:"incoming_parent_span_id"`
	IncomingSampled      string `json:"incoming_sampled"`
	TraceIDMatches       bool   `json:"trace_id_matches"`
	SpanIDMatches        bool   `json:"span_id_matches"`
}

type httpClientResponse struct {
	Status            string          `json:"status"`
	Sampled           bool            `json:"sampled"`
	TraceID           string          `json:"trace_id"`
	SpanID            string          `json:"span_id"`
	DownstreamStatus  int             `json:"downstream_status"`
	TraceIDMatches    bool            `json:"trace_id_matches"`
	ParentSpanMatches bool            `json:"parent_span_matches"`
	Propagated        bool            `json:"propagated"`
	Downstream        json.RawMessage `json:"downstream"`
}

// onHttpClient makes a real instrumented HTTP call to the downstream server and
// reports whether the trace context survived the hop. It is the local proof of
// cross-process propagation; the collector side is proven by the transport-log
// checks in run_e2e.sh.
func onHttpClient(w http.ResponseWriter, r *http.Request) {
	defer track()()
	tracer := newSpan(r)

	path := "/trace"
	if r.URL.Query().Has("error") {
		path = "/error"
	}
	url := "http://" + httpTarget + path

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		setTraceHeaders(w, tracer)
		e2e.WriteJSON(w, http.StatusInternalServerError, map[string]string{"status": "error"})
		finishSpan(w, r, tracer, http.StatusInternalServerError)
		return
	}
	req.Header.Set("User-Agent", "pinpoint-go-e2e")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", r.Header.Get("X-Request-ID"))
	req.AddCookie(&http.Cookie{Name: "session_id", Value: "e2e-session"})
	req.AddCookie(&http.Cookie{Name: "token", Value: "e2e-token"})
	// An unsampled inbound request must keep telling the next hop to skip
	// sampling, so the decision travels the whole chain.
	req = pinpoint.RequestWithTracerContext(req, tracer)

	resp, err := pphttp.DoClient(httpClient.Do, req)

	body := httpClientResponse{
		Status:     "ok",
		Sampled:    tracer.IsSampled(),
		TraceID:    tracer.TransactionId().String(),
		SpanID:     e2e.SpanIDString(tracer),
		Downstream: json.RawMessage("{}"),
	}
	status := http.StatusOK
	if err != nil {
		body.Status = "error"
		status = http.StatusBadGateway
		tracer.Span().SetError(err)
	} else {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		body.DownstreamStatus = resp.StatusCode
		if len(raw) > 0 {
			body.Downstream = json.RawMessage(raw)
		}
		var parsed downstreamBody
		json.Unmarshal(raw, &parsed)
		body.TraceIDMatches = resp.Header.Get(e2e.HeaderTraceID) == tracer.TransactionId().String()
		body.ParentSpanMatches = parsed.IncomingParentSpanID == e2e.SpanIDString(tracer)
		body.Propagated = tracer.IsSampled() && body.TraceIDMatches &&
			body.ParentSpanMatches && parsed.TraceIDMatches && parsed.SpanIDMatches
	}

	setTraceHeaders(w, tracer)
	e2e.WriteJSON(w, status, body)
	finishSpan(w, r, tracer, status)
}
