package pinpoint

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"
)

type noopAgent struct {
	config *Config
}

var defaultNoopAgent = &noopAgent{}

// initNoopAgent must run after initConfig: defaultConfig reads cfgBaseMap, and
// package variable initialization happens before any init function.
func initNoopAgent() {
	defaultNoopAgent.config = defaultConfig()
}

// NoopAgent returns a Agent that doesn't collect tracing data.
func NoopAgent() Agent {
	return defaultNoopAgent
}

func (agent *noopAgent) NewSpanTracer(operation string, rpcName string) Tracer {
	return NoopTracer()
}

func (agent *noopAgent) NewSpanTracerWithReader(operation string, rpcName string, reader DistributedTracingContextReader) Tracer {
	return NoopTracer()
}

func (agent *noopAgent) Enable() bool {
	return false
}

func (agent *noopAgent) Config() *Config {
	return agent.config
}

func (agent *noopAgent) Shutdown() {
}

type noopSpan struct {
	agent       *agent
	cfg         *configSnapshot
	spanId      int64
	startTime   time.Time
	rpcName     string
	goroutineId int64
	// realTimeTracked: see span.realTimeTracked. Plain like goroutineId, which
	// the same start/end pair writes and reads.
	realTimeTracked bool
	withStats       atomic.Bool
	unsampled       bool
	urlStat         *UrlStatEntry
	statusErr       atomic.Int32
	// traceRoot is the unsampled span holding the statistics, nil when this
	// span is the root itself. An unsampled async child keeps no statistics of
	// its own, so a failure it records must land on the root - Java's
	// continueDisableAsyncContextTraceObject hands the child the parent's
	// LocalTraceRoot for the same reason (DefaultBaseTraceFactory.java:139-145),
	// and DisableSpanRecorder writes the failure into traceRoot.getShared().
	traceRoot *noopSpan

	noopSe      noopSpanEvent
	annotations noopAnnotation
}

// defaultNoopSpan is a shared singleton: it stands for "there is no trace at
// all" (agent disabled, excluded URL, no tracer in the context), so it carries
// no per-request state and every field stays at its zero value. Read its
// fields freely; writing one is a data race across concurrent requests.
var defaultNoopSpan = noopSpan{}

// root returns the unsampled span carrying the request's statistics.
func (span *noopSpan) root() *noopSpan {
	if span.traceRoot != nil {
		return span.traceRoot
	}
	return span
}

// NoopTracer returns a Tracer that doesn't collect tracing data.
func NoopTracer() Tracer {
	return &defaultNoopSpan
}

// newUnSampledSpan returns the span for a real request that lost the sampling
// decision. Unlike the noop singleton it stands for an actual transaction, so
// it collects response-time and URL statistics (withStats) and propagates the
// decision downstream (unsampled); see Inject.
func newUnSampledSpan(agent *agent, rpcName string) *noopSpan {
	span := noopSpan{}
	span.agent = agent
	span.cfg = agent.config.load()
	span.spanId = generateSpanId()
	span.startTime = time.Now()
	span.rpcName = rpcName
	span.withStats.Store(true)
	span.unsampled = true

	addUnSampledActiveSpan(&span)

	return &span
}

func (span *noopSpan) EndSpan() {
	// A second EndSpan must not double-count; the swap also orders EndSpan
	// against SetError/SetFailure racing from other goroutines.
	if span.withStats.CompareAndSwap(true, false) {
		dropUnSampledActiveSpan(span)
		endTime := time.Now()
		elapsed := endTime.UnixMilli() - span.startTime.UnixMilli()
		span.agent.stats.collectResponseTime(elapsed)
		if span.urlStat != nil {
			span.agent.enqueueUrlStat(&urlStat{entry: span.urlStat, endTime: endTime, elapsed: elapsed, statusErr: int(span.statusErr.Load())})
		}
	}
}

func (span *noopSpan) NewSpanEvent(operationName string) Tracer {
	return span
}

// The child carries the parent's unsampled marker so calls made from an async
// goroutine still tell the callee not to trace, and a link to the root so a
// failure it records reaches the request's statistics - but never withStats:
// the statistics belong to the request's own span, which alone must end them.
func (span *noopSpan) NewAsyncSpan() Tracer {
	return &noopSpan{unsampled: span.unsampled, traceRoot: span.root()}
}

func (span *noopSpan) NewGoroutineTracer() Tracer {
	return &noopSpan{unsampled: span.unsampled, traceRoot: span.root()}
}

func (span *noopSpan) WrapGoroutine(goroutineName string, goroutine func(context.Context), ctx context.Context) func() {
	asyncSpan := span.NewGoroutineTracer()

	var newCtx context.Context
	if ctx == nil {
		newCtx = NewContext(context.Background(), asyncSpan)
	} else {
		newCtx = NewContext(ctx, asyncSpan)
	}

	return func() {
		goroutine(newCtx)
	}
}

func (span *noopSpan) EndSpanEvent() {}

func (span *noopSpan) TransactionId() TransactionId {
	return TransactionId{"Noop", 0, 0}
}

func (span *noopSpan) SpanId() int64 {
	return span.spanId
}

func (span *noopSpan) AsyncSpanId() string {
	return fmt.Sprintf("%d^Noop", span.spanId)
}

func (span *noopSpan) Span() SpanRecorder {
	return span
}

func (span *noopSpan) SpanEvent() SpanEventRecorder {
	return &span.noopSe
}

// SetError fails the URL stat of an unsampled request, as the Java agent's
// DisableSpanRecorder.recordException marks the span level (the span event
// level, DisableSpanEventRecorder, stays a no-op). Only the failure flag is
// kept: the span itself is never sent. Span.IgnoreErrors applies here too, or
// an excluded error would fail the URL stat of unsampled requests while
// sparing sampled ones, and Span.ErrorMark / Span.ErrorMarkExclude apply for
// the same reason - the category is ErrorCategoryException, as on a sampled
// span.
func (span *noopSpan) SetError(e error, errorName ...string) {
	root := span.root()
	if e == nil || !root.withStats.Load() {
		return // see SetFailure for why the singleton is never written
	}
	errName := errorTypeName(e)
	if len(errorName) > 0 {
		errName = errorName[0]
	}
	if !root.cfg.ignoreError(e, errName) && root.cfg.marksError(ErrorCategoryException) {
		root.statusErr.Store(1)
	}
}

// SetFailure fails the URL stat of an unsampled request under the category its
// caller names, ErrorCategoryUnknown when it names none. A category
// Span.ErrorMark or Span.ErrorMarkExclude disabled leaves the request a
// success, as it does on a sampled span: the two paths feed the same URL
// statistics.
func (span *noopSpan) SetFailure(category ...ErrorCategory) {
	// Write only on per-request unsampled spans. The defaultNoopSpan singleton
	// is shared by every tracer-less request, so writing its field here is a
	// data race between concurrent handlers (e.g. two 5xx responses) - and its
	// statusErr is never read anyway. An async child of the singleton resolves
	// to the singleton here, so that guard covers it too.
	root := span.root()
	if root.withStats.Load() && root.cfg.marksError(firstErrorCategory(category)) {
		root.statusErr.Store(1)
	}
}

func (span *noopSpan) SetServiceType(typ int32) {}

func (span *noopSpan) SetRpcName(rpc string) {}

func (span *noopSpan) SetRemoteAddress(remoteAddress string) {}

func (span *noopSpan) SetEndPoint(endPoint string) {}

func (span *noopSpan) SetAcceptorHost(host string) {}

// Inject propagates the sampling decision only when there is a decision to
// propagate. An unsampled span is a real transaction that lost sampling, so it
// sends "s0" to keep the callee from sampling the transaction back into
// existence. The noop singleton has no transaction behind it - an excluded
// URL, a batch job, a context with no tracer - and writes nothing, leaving the
// callee free to start a transaction of its own.
func (span *noopSpan) Inject(writer DistributedTracingContextWriter) {
	if writer != nil && span.unsampled {
		writer.Set(HeaderSampled, "s0")
	}
}

func (span *noopSpan) Extract(reader DistributedTracingContextReader) {}

func (span *noopSpan) Annotations() Annotation {
	return &span.annotations
}

func (span *noopSpan) SetLogging(logInfo int32) {}

func (span *noopSpan) IsSampled() bool {
	return false
}

// collectUrlStat follows the same first-wins policy as span.collectUrlStat
// (see mergeUrlStat); withStats keeps the process-wide singleton from being
// written to.
func (span *noopSpan) collectUrlStat(stat *UrlStatEntry, force bool) {
	if span.withStats.Load() && span.cfg.collectUrlStat {
		span.urlStat = mergeUrlStat(span.urlStat, stat, force)
	}
}

func (span *noopSpan) AddMetric(metric string, value interface{}) {
	if metric == MetricURLStat || metric == MetricURLStatForce {
		if entry, ok := value.(*UrlStatEntry); ok && entry != nil {
			span.collectUrlStat(entry, metric == MetricURLStatForce)
		}
	}
}

func (span *noopSpan) JsonString() []byte {
	b, _ := json.Marshal(span)
	return b
}

type noopSpanEvent struct {
	annotations noopAnnotation
}

var defaultNoopSpanEvent = noopSpanEvent{}

func (se *noopSpanEvent) SetError(e error, errorName ...string) {}

func (se *noopSpanEvent) SetServiceType(typ int32) {}

func (se *noopSpanEvent) SetDestination(id string) {}

func (se *noopSpanEvent) SetEndPoint(endPoint string) {}

func (se *noopSpanEvent) SetSQL(sql string, args string) {}

func (se *noopSpanEvent) Annotations() Annotation {
	return &se.annotations
}

func (se *noopSpanEvent) FixDuration(start time.Time, end time.Time) {}

type noopAnnotation struct{}

func (a *noopAnnotation) AppendInt(key int32, i int32) {}

func (a *noopAnnotation) AppendLong(key int32, l int64) {}

func (a *noopAnnotation) AppendString(key int32, s string) {}

func (a *noopAnnotation) AppendStringString(key int32, s1 string, s2 string) {}

func (a *noopAnnotation) AppendIntStringString(key int32, i int32, s1 string, s2 string) {}

func (a *noopAnnotation) AppendBytesStringString(key int32, b []byte, s1 string, s2 string) {}

func (a *noopAnnotation) AppendLongIntIntByteByteString(key int32, l int64, i1 int32, i2 int32, b1 int32, b2 int32, s string) {
}

type noopDistributedTracingContextReader struct{}

func (r *noopDistributedTracingContextReader) Get(key string) string {
	return ""
}
