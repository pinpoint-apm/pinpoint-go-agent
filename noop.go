package pinpoint

import (
	"context"
	"time"
)

type noopAgent struct {
	config *Config
}

var defaultNoopAgent = &noopAgent{
	config: defaultConfig(),
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
	spanId      int64
	startTime   time.Time
	rpcName     string
	goroutineId int64
	withStats   bool
	urlStat     *UrlStatEntry

	noopSe      noopSpanEvent
	annotations noopAnnotation
}

var defaultNoopSpan = noopSpan{}

// NoopTracer returns a Tracer that doesn't collect tracing data.
func NoopTracer() Tracer {
	return &defaultNoopSpan
}

func newUnSampledSpan(agent *agent, rpcName string) *noopSpan {
	span := noopSpan{}
	span.agent = agent
	span.spanId = generateSpanId()
	span.startTime = time.Now()
	span.rpcName = rpcName
	span.withStats = true

	addUnSampledActiveSpan(&span)

	return &span
}

func (span *noopSpan) EndSpan() {
	if span.withStats {
		dropUnSampledActiveSpan(span)
		endTime := time.Now()
		elapsed := endTime.Sub(span.startTime).Milliseconds()
		collectResponseTime(elapsed)
		if span.urlStat != nil {
			span.agent.enqueueUrlStat(&urlStat{entry: span.urlStat, endTime: endTime, elapsed: elapsed})
		}
	}
}

func (span *noopSpan) NewSpanEvent(operationName string) Tracer {
	return span
}

func (span *noopSpan) NewAsyncSpan() Tracer {
	return &noopSpan{}
}

func (span *noopSpan) NewGoroutineTracer() Tracer {
	return &noopSpan{}
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

func (span *noopSpan) Span() SpanRecorder {
	return span
}

func (span *noopSpan) SpanEvent() SpanEventRecorder {
	return &span.noopSe
}

func (span *noopSpan) SetError(e error) {}

func (span *noopSpan) SetFailure() {}

func (span *noopSpan) SetServiceType(typ int32) {}

func (span *noopSpan) SetRpcName(rpc string) {}

func (span *noopSpan) SetRemoteAddress(remoteAddress string) {}

func (span *noopSpan) SetEndPoint(endPoint string) {}

func (span *noopSpan) SetAcceptorHost(host string) {}

func (span *noopSpan) Inject(writer DistributedTracingContextWriter) {
	if writer != nil {
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

func (span *noopSpan) collectUrlStat(stat *UrlStatEntry) {
	if span.withStats && span.agent.config.collectUrlStat {
		if stat.Url == "" {
			stat.Url = "UNKNOWN_URL"
		}
		span.urlStat = stat
	}
}

func (span *noopSpan) AddMetric(metric string, value interface{}) {
	if metric == MetricURLStat {
		span.collectUrlStat(value.(*UrlStatEntry))
	}
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

func (a *noopAnnotation) AppendString(key int32, s string) {}

func (a *noopAnnotation) AppendStringString(key int32, s1 string, s2 string) {}

func (a *noopAnnotation) AppendIntStringString(key int32, i int32, s1 string, s2 string) {}

func (a *noopAnnotation) AppendLongIntIntByteByteString(key int32, l int64, i1 int32, i2 int32, b1 int32, b2 int32, s string) {
}

type noopDistributedTracingContextReader struct{}

func (r *noopDistributedTracingContextReader) Get(key string) string {
	return ""
}
