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
	// NewSpanTracer returns a span Tracer indicating the start of a transaction.
	// A span is generated according to a given sampling policy, and trace data is not collected if not sampled.
	NewSpanTracer(operation string, rpcName string) Tracer

	// NewSpanTracerWithReader returns a span Tracer that continues a transaction passed from the previous node.
	// A span is generated according to a given sampling policy, and trace data is not collected if not sampled.
	// Distributed tracing headers are extracted from the reader. If it is empty, new transaction is started.
	NewSpanTracerWithReader(operation string, rpcName string, reader DistributedTracingContextReader) Tracer

	// Enable returns whether the agent is in an operational state.
	Enable() bool

	// Shutdown stops all related goroutines managing this agent.
	// After Shutdown is called, The agent will never collect tracing data again.
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

// SpanRecorder records the collected data in the fields of Span.
type SpanRecorder interface {
	// SetServiceType sets the type of service.
	SetServiceType(typ int32)

	// SetError Record an error and indicate that operation has failed.
	SetError(e error)

	// SetFailure indicate that operation has failed.
	SetFailure()

	// SetRpcName sets the name of RPC.
	// This value is displayed as the path of the span on the pinpoint web screen.
	SetRpcName(rpc string)

	// SetRemoteAddress sets the remote address.
	SetRemoteAddress(remoteAddress string)

	// SetEndPoint sets the end point of RPC.
	SetEndPoint(endPoint string)

	// SetAcceptorHost sets the host of acceptor.
	SetAcceptorHost(host string)

	// SetLogging sets whether the Span has been logged.
	SetLogging(logInfo int32)

	// Annotations returns annotations that the Span holds.
	Annotations() Annotation
}

// SpanEventRecorder records the collected data in the fields of SpanEvent.
type SpanEventRecorder interface {
	// SetServiceType sets the type of service.
	SetServiceType(typ int32)

	// SetDestination sets the destination of operation.
	SetDestination(id string)

	// SetEndPoint sets the end point of operation.
	SetEndPoint(endPoint string)

	// SetError Record an error and indicate that operation has failed.
	SetError(e error, errorName ...string)

	// SetSQL records the SQL string and bind variables.
	SetSQL(sql string, args string)

	// Annotations returns annotations that the SpanEvent holds.
	Annotations() Annotation

	// FixDuration fixes the elapsed time of operation.
	FixDuration(start time.Time, end time.Time)
}

// Annotation is a key-value pair and used to annotate Span and SpanEvent with more information.
type Annotation interface {
	// AppendInt records an integer value to annotation.
	AppendInt(key int32, i int32)

	// AppendString records a string value to annotation.
	AppendString(key int32, s string)

	// AppendStringString records two string values to annotation.
	AppendStringString(key int32, s1 string, s2 string)

	// AppendIntStringString records an integer value and two string values to annotation.
	AppendIntStringString(key int32, i int32, s1 string, s2 string)

	// AppendLongIntIntByteByteString records a long integer value, two integer value, two byte value and a string value to annotation.
	AppendLongIntIntByteByteString(key int32, l int64, i1 int32, i2 int32, b1 int32, b2 int32, s string)
}

// DistributedTracingContextReader reads distributed tracing headers from carrier.
type DistributedTracingContextReader interface {
	// Get returns the value of a given key from carrier.
	Get(key string) string
}

// DistributedTracingContextWriter writes distributed tracing headers to carrier.
type DistributedTracingContextWriter interface {
	// Set sets a given key-value pair to carrier.
	Set(key string, value string)
}

// TransactionId represents that different RPCs are associated with each other as a single transaction.
// TransactionId consists of AgentId, application startup time, and sequence number.
type TransactionId struct {
	AgentId   string
	StartTime int64
	Sequence  int64
}

// String returns transaction id string.
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
