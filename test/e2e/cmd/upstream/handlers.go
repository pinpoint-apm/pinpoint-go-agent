package main

import (
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	pphttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"github.com/pinpoint-apm/pinpoint-go-agent/test/e2e/internal/e2e"
)

var (
	totalRequests  atomic.Uint64
	activeRequests atomic.Int64
	startTime      = time.Now()
)

// track counts a request for /stats, mirroring the C++ server's RequestTracker.
func track() func() {
	totalRequests.Add(1)
	activeRequests.Add(1)
	return func() { activeRequests.Add(-1) }
}

// newSpan starts the server span for a request the same way the http plugin's
// wrapped handler would, but keeps the tracer in hand so each endpoint can
// decide what to record.
func newSpan(r *http.Request) pinpoint.Tracer {
	return pphttp.NewHttpServerTracer(r, "go-e2e-upstream")
}

// finishSpan records the response and ends the span. The trace headers are set
// before the body is written so a caller can read them off the response.
func finishSpan(w http.ResponseWriter, r *http.Request, tracer pinpoint.Tracer, status int) {
	pphttp.CollectUrlStat(tracer, r.URL.Path, r.Method, status)
	pphttp.RecordHttpServerResponse(tracer, status, w.Header())
	tracer.EndSpan()
}

func setTraceHeaders(w http.ResponseWriter, tracer pinpoint.Tracer) {
	w.Header().Set(e2e.HeaderTraceID, tracer.TransactionId().String())
	w.Header().Set(e2e.HeaderSpanID, e2e.SpanIDString(tracer))
}

// onSimple is the minimal traced request: one span with one event.
func onSimple(w http.ResponseWriter, r *http.Request) {
	defer track()()
	tracer := newSpan(r)
	tracer.NewSpanEvent("simple_work")
	tracer.EndSpanEvent()

	setTraceHeaders(w, tracer)
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
	finishSpan(w, r, tracer, http.StatusOK)
}

// onDeep nests span events until (and past) the configured depth limit. Past
// the limit every further event is discarded, but a call made from that depth
// must still carry a complete trace context or the distributed trace would be
// cut there. Opt in with ?inject=1 so the load workload's shallow /deep calls
// keep their existing shape.
func onDeep(w http.ResponseWriter, r *http.Request) {
	defer track()()
	tracer := newSpan(r)
	depth := e2e.IntParam(r, "depth", 20, 1, 256)

	for i := 0; i < depth; i++ {
		tracer.NewSpanEvent("deep_level_" + strconv.Itoa(i))
	}

	overflowContext := false
	if r.URL.Query().Has("inject") {
		tracer.SpanEvent().SetDestination("deep-overflow-target:8080")
		carrier := headerCarrier{}
		tracer.Inject(carrier)
		overflowContext = carrier.has(pinpoint.HeaderTraceId) &&
			carrier.has(pinpoint.HeaderSpanId) &&
			carrier.has(pinpoint.HeaderParentSpanId)
	}

	for i := 0; i < depth; i++ {
		tracer.EndSpanEvent()
	}

	setTraceHeaders(w, tracer)
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "depth=%d overflow_context=%t", depth, overflowContext)
	finishSpan(w, r, tracer, http.StatusOK)
}

// onWide records many sequential events, crossing the sequence limit.
func onWide(w http.ResponseWriter, r *http.Request) {
	defer track()()
	tracer := newSpan(r)
	width := e2e.IntParam(r, "width", 50, 1, 10000)

	for i := 0; i < width; i++ {
		tracer.NewSpanEvent("wide_event_" + strconv.Itoa(i))
		tracer.EndSpanEvent()
	}

	setTraceHeaders(w, tracer)
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "width=%d", width)
	finishSpan(w, r, tracer, http.StatusOK)
}

// onAnnotated records every annotation shape the public API offers.
func onAnnotated(w http.ResponseWriter, r *http.Request) {
	defer track()()
	tracer := newSpan(r)

	for i := 0; i < 10; i++ {
		tracer.NewSpanEvent("annotated_op_" + strconv.Itoa(i))
		se := tracer.SpanEvent()
		se.SetServiceType(pinpoint.ServiceTypeGoFunction)
		se.SetDestination("test-dest-" + strconv.Itoa(i))
		se.SetEndPoint("test-endpoint-" + strconv.Itoa(i))
		a := se.Annotations()
		a.AppendString(pinpoint.AnnotationHttpUrl, "/annotated/"+strconv.Itoa(i))
		a.AppendInt(pinpoint.AnnotationHttpStatusCode, 200)
		a.AppendStringString(pinpoint.AnnotationHttpRequestHeader,
			"X-Custom-"+strconv.Itoa(i), "value-"+strconv.Itoa(i))
		tracer.EndSpanEvent()
	}

	setTraceHeaders(w, tracer)
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "annotated")
	finishSpan(w, r, tracer, http.StatusOK)
}

// onMixed combines nesting, annotations, SQL-shaped events and a goroutine
// span. The goroutine is joined so the endpoint has deterministic completion.
func onMixed(w http.ResponseWriter, r *http.Request) {
	defer track()()
	tracer := newSpan(r)

	tracer.NewSpanEvent("db_query")
	tracer.SpanEvent().SetServiceType(pinpoint.ServiceTypeMysqlExecuteQuery)
	tracer.SpanEvent().SetEndPoint("localhost:3306")
	tracer.SpanEvent().SetDestination("test")
	tracer.SpanEvent().SetSQL("SELECT * FROM users WHERE id = ?", "42")
	tracer.NewSpanEvent("db_parse")
	tracer.EndSpanEvent()
	tracer.EndSpanEvent()

	tracer.NewSpanEvent("http_client_call")
	tracer.SpanEvent().SetServiceType(pinpoint.ServiceTypeGoHttpClient)
	tracer.SpanEvent().SetDestination("downstream-service")
	tracer.SpanEvent().SetEndPoint("downstream:8080")
	tracer.SpanEvent().Annotations().AppendString(pinpoint.AnnotationHttpUrl, "http://downstream:8080/api/data")
	tracer.SpanEvent().Annotations().AppendInt(pinpoint.AnnotationHttpStatusCode, 200)
	tracer.EndSpanEvent()

	tracer.NewSpanEvent("prepare_async")
	async := tracer.NewGoroutineTracer()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rand.Intn(20)+1; i++ {
			async.NewSpanEvent("async_work_" + strconv.Itoa(i))
			time.Sleep(time.Duration(rand.Intn(50)+1) * time.Millisecond)
			async.EndSpanEvent()
		}
		async.EndSpan()
	}()
	tracer.EndSpanEvent()

	for i := 0; i < rand.Intn(20)+1; i++ {
		tracer.NewSpanEvent("post_process_" + strconv.Itoa(i))
		time.Sleep(time.Duration(rand.Intn(50)+1) * time.Millisecond)
		tracer.EndSpanEvent()
	}
	wg.Wait()

	setTraceHeaders(w, tracer)
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "mixed")
	finishSpan(w, r, tracer, http.StatusOK)
}

// onError returns an intentional 500 with an error span.
func onError(w http.ResponseWriter, r *http.Request) {
	defer track()()
	tracer := newSpan(r)

	tracer.NewSpanEvent("failing_operation")
	tracer.SpanEvent().SetError(errors.New("simulated error: connection timeout"), "ConnectionTimeout")
	tracer.EndSpanEvent()
	tracer.Span().SetError(errors.New("Internal Server Error"))

	setTraceHeaders(w, tracer)
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprint(w, "error")
	finishSpan(w, r, tracer, http.StatusInternalServerError)
}

// onFilterProbe reports whether the URL/method filters admitted this request.
// A filtered request gets the plain noop tracer, whose span id is 0; an
// unsampled span still carries a real id, so the id separates filtering from a
// sampling decision.
func onFilterProbe(w http.ResponseWriter, r *http.Request) {
	defer track()()
	tracer := newSpan(r)
	setTraceHeaders(w, tracer)
	e2e.WriteJSON(w, http.StatusOK, map[string]any{
		"path":   r.URL.Path,
		"traced": tracer.SpanId() != 0,
	})
	finishSpan(w, r, tracer, http.StatusOK)
}

type featuresResponse struct {
	Status              string `json:"status"`
	Sampled             bool   `json:"sampled"`
	TraceID             string `json:"trace_id"`
	SpanID              string `json:"span_id"`
	ActiveEventObserved bool   `json:"active_event_observed"`
	LoggingContext      bool   `json:"logging_context"`
	ContextInjected     bool   `json:"context_injected"`
	AsyncComplete       bool   `json:"async_complete"`
	AsyncTraceMatches   bool   `json:"async_trace_matches"`
}

// onFeatures is deterministic coverage of the public tracing API. The response
// exposes locally verifiable invariants; the serialized metadata is then sent
// to the live collector by the agent.
func onFeatures(w http.ResponseWriter, r *http.Request) {
	defer track()()
	tracer := newSpan(r)
	tracer.Span().SetServiceType(pinpoint.ServiceTypeGoApp)
	tracer.Span().SetAcceptorHost(r.Host)
	tracer.Span().SetLogging(pinpoint.Logged)

	a := tracer.Span().Annotations()
	a.AppendInt(9100, 42)
	a.AppendLong(9101, 1234567890123)
	a.AppendString(9102, "go-e2e-feature-span")
	a.AppendStringString(9103, "feature-key", "feature-value")

	tracer.NewSpanEvent("feature-event")
	activeEventObserved := tracer.SpanEvent() != nil
	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeGoFunction)
	se.SetDestination("feature-destination")
	se.SetEndPoint("feature-endpoint:1234")
	se.FixDuration(time.Now().Add(-time.Millisecond), time.Now())
	ea := se.Annotations()
	ea.AppendInt(9200, -1)
	ea.AppendLong(9201, -9876543210)
	ea.AppendString(9202, "event-string")
	ea.AppendStringString(9203, "event-key", "event-value")
	ea.AppendIntStringString(9204, 7, "int-string-1", "int-string-2")
	ea.AppendBytesStringString(9205, []byte{0xde, 0xad, 0xbe, 0xef}, "bytes-1", "bytes-2")
	ea.AppendLongIntIntByteByteString(9206, 1234, 1, 2, 3, 4, "long-int-int-byte-byte")
	tracer.EndSpanEvent()

	tracer.NewSpanEvent("feature-sql")
	tracer.SpanEvent().SetServiceType(pinpoint.ServiceTypeMysqlExecuteQuery)
	tracer.SpanEvent().SetDestination("feature-db")
	tracer.SpanEvent().SetEndPoint("127.0.0.1:3306")
	tracer.SpanEvent().SetSQL("SELECT name FROM users WHERE id = ? AND role = ? /* e2e */", "17, admin")
	tracer.EndSpanEvent()

	// Error.TraceCallStack is on, so this also publishes exception metadata
	// with the Go call stack captured at SetError.
	tracer.NewSpanEvent("feature-callstack-error")
	tracer.SpanEvent().SetError(errors.New("deterministic feature failure"), "FeatureFailure")
	tracer.EndSpanEvent()

	logging := headerCarrier{}
	logging.Set(pinpoint.LogTransactionIdKey, tracer.TransactionId().String())
	logging.Set(pinpoint.LogSpanIdKey, e2e.SpanIDString(tracer))
	loggingContext := logging.has(pinpoint.LogTransactionIdKey) && logging.has(pinpoint.LogSpanIdKey)

	injected := headerCarrier{}
	tracer.NewSpanEvent("feature-context-injection")
	tracer.SpanEvent().SetDestination("feature-context-target:8080")
	tracer.Inject(injected)
	tracer.EndSpanEvent()
	contextInjected := injected.has(pinpoint.HeaderTraceId) &&
		injected.has(pinpoint.HeaderSpanId) &&
		injected.has(pinpoint.HeaderParentSpanId)

	// A goroutine tracer hangs off the event on top of the stack, so an event
	// must still be open when it is created.
	tracer.NewSpanEvent("feature-async-invocation")
	async := tracer.NewGoroutineTracer()
	parentTraceID := tracer.TransactionId().String()
	var asyncComplete, asyncTraceMatches bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		async.NewSpanEvent("feature-async-work")
		async.SpanEvent().Annotations().AppendString(pinpoint.AnnotationApi, "feature-async-work")
		async.EndSpanEvent()
		asyncTraceMatches = async.TransactionId().String() == parentTraceID
		async.EndSpan()
		asyncComplete = true
	}()
	wg.Wait()
	tracer.EndSpanEvent()

	setTraceHeaders(w, tracer)
	w.Header().Set("X-Response-Time", "2ms")
	w.Header().Set("X-Request-ID", r.Header.Get("X-Request-ID"))
	e2e.WriteJSON(w, http.StatusOK, featuresResponse{
		Status:              "ok",
		Sampled:             tracer.IsSampled(),
		TraceID:             tracer.TransactionId().String(),
		SpanID:              e2e.SpanIDString(tracer),
		ActiveEventObserved: activeEventObserved,
		LoggingContext:      loggingContext,
		ContextInjected:     contextInjected,
		AsyncComplete:       asyncComplete,
		AsyncTraceMatches:   asyncTraceMatches,
	})
	finishSpan(w, r, tracer, http.StatusOK)
}

// onSamplingProbe reports how many of count fresh traces the current sampler
// admits. It is how the smoke test observes a sampling reload.
func onSamplingProbe(w http.ResponseWriter, r *http.Request) {
	defer track()()
	count := e2e.IntParam(r, "count", 20, 1, 1000)
	sampled := probeSampled(count, "/sampling-probe/")
	e2e.WriteJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"total":     count,
		"sampled":   sampled,
		"unsampled": count - sampled,
	})
}

func probeSampled(count int, prefix string) int {
	sampled := 0
	for i := 0; i < count; i++ {
		tracer := pinpoint.GetAgent().NewSpanTracer("sampling-probe", prefix+strconv.Itoa(i))
		if tracer.IsSampled() {
			sampled++
		}
		tracer.EndSpan()
	}
	return sampled
}

func onStats(w http.ResponseWriter, r *http.Request) {
	elapsed := int64(time.Since(startTime).Seconds())
	total := totalRequests.Load()
	rps := 0.0
	if elapsed > 0 {
		rps = float64(total) / float64(elapsed)
	}
	e2e.WriteJSON(w, http.StatusOK, map[string]any{
		"uptime_seconds":      elapsed,
		"total_requests":      total,
		"active_requests":     activeRequests.Load(),
		"agent_enabled":       pinpoint.GetAgent().Enable(),
		"collector_host":      e2e.CollectorHost(),
		"requests_per_second": rps,
	})
}

func onReady(w http.ResponseWriter, r *http.Request) {
	enabled := pinpoint.GetAgent().Enable()
	status := http.StatusOK
	state := "ready"
	if !enabled {
		status = http.StatusServiceUnavailable
		state = "waiting_for_collector"
	}
	e2e.WriteJSON(w, status, map[string]any{
		"status":         state,
		"agent_enabled":  enabled,
		"collector_host": e2e.CollectorHost(),
	})
}

// headerCarrier is a distributed-tracing carrier backed by a plain map, used
// where the endpoint injects a context it then inspects itself.
type headerCarrier map[string]string

func (h headerCarrier) Get(key string) string { return h[key] }
func (h headerCarrier) Set(key, value string) { h[key] = value }
func (h headerCarrier) has(key string) bool   { _, ok := h[key]; return ok }
