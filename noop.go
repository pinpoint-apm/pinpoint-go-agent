package pinpoint

import (
	"time"
)

type noopSpan struct {
	agent       Agent
	noopSe      noopSpanEvent
	annotations noopannotation
}

func newNoopSpan(agent Agent) Tracer {
	span := noopSpan{}
	span.agent = agent
	return &span
}

func (span *noopSpan) EndSpan() {}

func (span *noopSpan) NewSpanEvent(operationName string) Tracer {
	return span
}

func (span *noopSpan) NewAsyncSpan() Tracer {
	asyncSpan := noopSpan{}
	asyncSpan.agent = span.agent
	return &asyncSpan
}

func (span *noopSpan) EndSpanEvent() {}

func (span *noopSpan) TransactionId() TransactionId {
	return TransactionId{span.agent.Config().AgentId, span.agent.StartTime(), -1}
}

func (span *noopSpan) SpanId() int64 {
	return -1
}

func (span *noopSpan) Span() SpanRecorder {
	return span
}

func (span *noopSpan) SpanEvent() SpanEventRecorder {
	return &span.noopSe
}

func (span *noopSpan) SetError(e error) {}

func (span *noopSpan) SetApiId(id int32) {}

func (span *noopSpan) SetServiceType(typ int32) {}

func (span *noopSpan) SetRpcName(rpc string) {}

func (span *noopSpan) SetRemoteAddress(remoteAddress string) {}

func (span *noopSpan) SetEndPoint(endPoint string) {}

func (span *noopSpan) SetAcceptorHost(host string) {}

func (span *noopSpan) Inject(writer DistributedTracingContextWriter) {
	writer.Set(HttpSampled, "s0")
}

func (span *noopSpan) Extract(reader DistributedTracingContextReader) {}

func (span *noopSpan) Annotations() Annotation {
	return &span.annotations
}

func (span *noopSpan) SetLogging(logInfo int32) {}

type noopSpanEvent struct {
	annotations noopannotation
}

func (se *noopSpanEvent) SetError(e error) {}

func (se *noopSpanEvent) SetApiId(id int32) {}

func (se *noopSpanEvent) SetServiceType(typ int32) {}

func (se *noopSpanEvent) SetDestination(id string) {}

func (se *noopSpanEvent) SetEndPoint(endPoint string) {}

func (se *noopSpanEvent) SetSQL(sql string) {}

func (span *noopSpanEvent) Annotations() Annotation {
	return &span.annotations
}

func (se *noopSpanEvent) FixDuration(start time.Time, end time.Time) {}

type noopannotation struct{}

func (a *noopannotation) AppendInt(key int32, i int32) {}

func (a *noopannotation) AppendString(key int32, s string) {}

func (a *noopannotation) AppendStringString(key int32, s1 string, s2 string) {}

func (a *noopannotation) AppendIntStringString(key int32, i int32, s1 string, s2 string) {}

func (a *noopannotation) AppendLongIntIntByteByteString(key int32, l int64, i1 int32, i2 int32, b1 int32, b2 int32, s string) {
}

type noopDistributedTracingContextReader struct{}

func (r *noopDistributedTracingContextReader) Get(key string) string {
	return ""
}
