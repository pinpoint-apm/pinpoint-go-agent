// downstream is the HTTP downstream of the end-to-end suite. It reports the
// trace context it received alongside the one it created, so the upstream can
// verify propagation locally.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	pphttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"github.com/pinpoint-apm/pinpoint-go-agent/test/e2e/internal/e2e"
)

type traceResponse struct {
	TraceID               string `json:"trace_id"`
	SpanID                string `json:"span_id"`
	Sampled               bool   `json:"sampled"`
	IncomingTraceID       string `json:"incoming_trace_id"`
	IncomingSpanID        string `json:"incoming_span_id"`
	IncomingParentSpanID  string `json:"incoming_parent_span_id"`
	IncomingSampled       string `json:"incoming_sampled"`
	IncomingParentAppName string `json:"incoming_parent_app_name"`
	TraceIDMatches        bool   `json:"trace_id_matches"`
	SpanIDMatches         bool   `json:"span_id_matches"`
}

func trace(w http.ResponseWriter, r *http.Request) {
	tracer := pphttp.NewHttpServerTracer(r, "go-e2e-http-downstream")
	defer tracer.EndSpan()

	event := tracer.NewSpanEvent("downstream_work")
	event.SpanEvent().SetServiceType(pinpoint.ServiceTypeGoFunction)
	event.SpanEvent().SetDestination("http-downstream")
	tracer.EndSpanEvent()

	status := http.StatusOK
	if r.URL.Path == "/error" {
		status = http.StatusServiceUnavailable
		tracer.Span().SetError(errors.New("downstream failed on purpose"))
	}
	pphttp.CollectUrlStat(tracer, r.URL.Path, r.Method, status)

	incomingTrace := r.Header.Get(pinpoint.HeaderTraceId)
	body := traceResponse{
		TraceID:               tracer.TransactionId().String(),
		SpanID:                e2e.SpanIDString(tracer),
		Sampled:               tracer.IsSampled(),
		IncomingTraceID:       incomingTrace,
		IncomingSpanID:        r.Header.Get(pinpoint.HeaderSpanId),
		IncomingParentSpanID:  r.Header.Get(pinpoint.HeaderParentSpanId),
		IncomingSampled:       r.Header.Get(pinpoint.HeaderSampled),
		IncomingParentAppName: r.Header.Get(pinpoint.HeaderParentApplicationName),
		TraceIDMatches:        incomingTrace != "" && incomingTrace == tracer.TransactionId().String(),
		SpanIDMatches:         r.Header.Get(pinpoint.HeaderSpanId) != "",
	}

	w.Header().Set(e2e.HeaderTraceID, body.TraceID)
	w.Header().Set(e2e.HeaderSpanID, body.SpanID)
	e2e.WriteJSON(w, status, body)
	pphttp.RecordHttpServerResponse(tracer, status, w.Header())
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	port := e2e.Port(os.Args[1:], 8091)

	e2e.ConfigureAgentEnvironment("go-e2e-http-downstream", "go-e2e-http-down")
	agent := e2e.StartAgent(e2e.ConfigFileOption())

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		e2e.WriteJSON(w, http.StatusOK, map[string]any{
			"agent_enabled":  pinpoint.GetAgent().Enable(),
			"collector_host": e2e.CollectorHost(),
		})
	})
	mux.HandleFunc("/echo", trace)
	mux.HandleFunc("/trace", trace)
	mux.HandleFunc("/error", trace)

	server := &http.Server{Addr: e2e.Addr(port), Handler: mux}
	// The runner posts this before falling back to SIGTERM, so the agent gets a
	// chance to flush instead of being killed mid-batch.
	mux.HandleFunc("/shutdown", func(w http.ResponseWriter, r *http.Request) {
		e2e.WriteJSON(w, http.StatusOK, map[string]string{"status": "shutting_down"})
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server.Shutdown(ctx)
		}()
	})

	log.Printf("HTTP downstream server starting on %s (collector=%s)", e2e.Addr(port), e2e.CollectorHost())
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("listen: %v", err)
	}
	agent.Shutdown()
}
