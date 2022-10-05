package pinpoint

import (
	"context"
	"fmt"
	"time"
)

// Agent instruments a application and makes spans and manages it.
// In Pinpoint, a transaction consists of a group of Spans.
// Each span represents a trace of a single logical node where the transaction has gone through.
// A span records important function invocations and their related data(arguments, return value, etc.)
// before encapsulating them as SpanEvents in a call stack like representation.
// The span itself and each of its SpanEvents represents a function invocation.
// Find out more about the concept of Pinpoint at the links below.
// https://pinpoint-apm.gitbook.io/pinpoint/documents/plugin-dev-guide
// https://pinpoint-apm.gitbook.io/pinpoint/want-a-quick-tour/techdetail
type Agent interface {
	NewSpanTracer(operation string, rpcName string) Tracer
	NewSpanTracerWithReader(operation string, rpcName string, reader DistributedTracingContextReader) Tracer
	Enable() bool
	Shutdown()
}

// Tracer instruments a single call stack of application and makes the result a single span.
type Tracer interface {
	// NewSpanEvent returns a span event.
	NewSpanEvent(operationName string) Tracer

	// NewAsyncSpan is deprecated. Use NewGoroutineTracer.
	NewAsyncSpan() Tracer

	// NewGoroutineTracer returns a tracer that tracks the call stack of a goroutine.
	NewGoroutineTracer() Tracer

	// WrapGoroutine generates a tracer that tracks a given goroutine and passes it in context.
	WrapGoroutine(goroutineName string, goroutine func(context.Context), ctx context.Context) func()

	// EndSpan completes the span and transmits it to the collector.
	// Sending a span is handled by a separate goroutine.
	EndSpan()

	// EndSpanEvent completes the span event.
	EndSpanEvent()

	// Inject injects distributed tracing headers to the writer.
	Inject(writer DistributedTracingContextWriter)

	// Extract extracts distributed tracing headers from the reader.
	Extract(reader DistributedTracingContextReader)

	// TransactionId returns the ID of the transaction containing the span.
	TransactionId() TransactionId

	// SpanId returns the ID of the span.
	SpanId() int64

	Span() SpanRecorder
	SpanEvent() SpanEventRecorder

	// IsSampled returns whether the span has been sampled.
	IsSampled() bool
}

type SpanRecorder interface {
	SetApiId(id int32)
	SetServiceType(typ int32)
	SetError(e error)
	SetFailure()
	SetRpcName(rpc string)
	SetRemoteAddress(remoteAddress string)
	SetEndPoint(endPoint string)
	SetAcceptorHost(host string)
	SetLogging(logInfo int32)
	Annotations() Annotation
}

type SpanEventRecorder interface {
	SetApiId(id int32)
	SetServiceType(typ int32)
	SetDestination(id string)
	SetEndPoint(endPoint string)
	SetError(e error, errorName ...string)
	SetSQL(sql string, args string)
	Annotations() Annotation
	FixDuration(start time.Time, end time.Time)
}

type Annotation interface {
	AppendInt(key int32, i int32)
	AppendString(key int32, s string)
	AppendStringString(key int32, s1 string, s2 string)
	AppendIntStringString(key int32, i int32, s1 string, s2 string)
	AppendLongIntIntByteByteString(key int32, l int64, i1 int32, i2 int32, b1 int32, b2 int32, s string)
}

type DistributedTracingContextReader interface {
	Get(key string) string
}

type DistributedTracingContextWriter interface {
	Set(key string, value string)
}

type TransactionId struct {
	AgentId   string
	StartTime int64
	Sequence  int64
}

func (tid TransactionId) String() string {
	return fmt.Sprintf("%s^%d^%d", tid.AgentId, tid.StartTime, tid.Sequence)
}

const (
	HttpTraceId                    = "Pinpoint-TraceID"
	HttpSpanId                     = "Pinpoint-SpanID"
	HttpParentSpanId               = "Pinpoint-pSpanID"
	HttpSampled                    = "Pinpoint-Sampled"
	HttpFlags                      = "Pinpoint-Flags"
	HttpParentApplicationName      = "Pinpoint-pAppName"
	HttpParentApplicationType      = "Pinpoint-pAppType"
	HttpParentApplicationNamespace = "Pinpoint-pAppNamespace"
	HttpHost                       = "Pinpoint-Host"

	LogTransactionIdKey = "PtxId"
	LogSpanIdKey        = "PspanId"
	Logged              = 1
	NotLogged           = 0

	ServiceTypeGoApp        = 1800
	ServiceTypeGoFunction   = 1801
	ServiceTypeGoHttpClient = 9401
	ServiceTypeAsync        = 100

	ApiTypeDefault    = 0
	ApiTypeWebRequest = 100
	ApiTypeInvocation = 200

	MaxApplicationNameLength = 24
	MaxAgentIdLength         = 24
	MaxAgentNameLength       = 255
	SamplingTypeCounter      = "COUNTER"
	SamplingTypePercent      = "PERCENT"
	SmaplingMaxPercentRate   = 100 * 100
)
