// Package pinpoint provides the APIs to instrument Go applications for Pinpoint (https://github.com/pinpoint-apm/pinpoint).
//
// This package enables you to monitor Go applications using Pinpoint.
// Go applications must be instrumented manually at the source code level,
// Developers can instrument Go applications using the APIs provided in this package.
// It has support for instrumenting Go’s built-in http package, database/sql drivers
// and plug-ins for popular frameworks and toolkits (like Gin and gRPC, ...).
//
// In Pinpoint, a transaction consists of a group of Spans.
// Each span represents a trace of a single logical node where the transaction has gone through.
// A span records important function invocations and their related data(arguments, return value, etc.)
// before encapsulating them as SpanEvents in a call stack like representation.
// The span itself and each of its SpanEvents represents a function invocation.
// Find out more about the concept of Pinpoint at the links below:
//   - https://pinpoint-apm.gitbook.io/pinpoint/documents/plugin-dev-guide
//   - https://pinpoint-apm.gitbook.io/pinpoint/want-a-quick-tour/techdetail
package pinpoint

import (
	"context"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// Agent instruments an application and makes spans and manages it.
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

	// Config returns the configuration associated with the agent.
	Config() *Config

	// Shutdown stops all related goroutines managing this agent.
	// After Shutdown is called, the agent will never collect tracing data again.
	//
	// Shutdown is the only path that sends the last data out. If it never
	// runs - the process is killed by a signal such as SIGTERM, or exits via
	// os.Exit, neither of which runs deferred functions - the spans still in
	// the span queue are never sent, and the collector never learns the
	// agent's end time, so the web UI keeps listing the agent as alive.
	// ShutdownOnSignal is the opt-in way to run it on a signal; nothing can
	// run it on os.Exit.
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

	// AddMetric adds a custom metric.
	AddMetric(metric string, value interface{})

	JsonString() []byte
	AsyncSpanId() string
}

// ErrorCategory is the cause that failed a transaction, carried as one bit of
// PSpan.err. The bit values are a wire contract, not an internal detail: the
// collector reads them to tell an exception apart from a failing HTTP status,
// must never be renumbered.
//
// request that threw and returned 5xx reports both causes. Span.ErrorMark and
// Span.ErrorMarkExclude decide which categories are allowed to fail a
// transaction at all.
type ErrorCategory int32

const (
	// ErrorCategoryUnknown is a failure with no cause attached: what
	// SetFailure records when its caller names no category, and what an agent
	// SimpleErrorRecorder, used when profiler.error.enable=false). It is
	// always enabled, whatever Span.ErrorMark and Span.ErrorMarkExclude say.
	ErrorCategoryUnknown ErrorCategory = 1 << 0

	// ErrorCategoryException is an error recorded through SetError, on the
	// span or on any of its events.
	ErrorCategoryException ErrorCategory = 1 << 1

	// ErrorCategoryHttpStatus is a response status the operator counts as a
	// failure (Http.Server.StatusCodeErrors).
	ErrorCategoryHttpStatus ErrorCategory = 1 << 2

	// ErrorCategorySql is the SQL.ErrorCount limit: a transaction that ran
	// that many statements.
	ErrorCategorySql ErrorCategory = 1 << 3
)

// SpanRecorder records the collected data in the fields of Span.
type SpanRecorder interface {
	// SetServiceType sets the type of service.
	SetServiceType(typ int32)

	// SetError Record an error and indicate that operation has failed.
	// The optional errorName names the error group in the UI, defaulting to
	// the error's Go type name.
	SetError(e error, errorName ...string)

	// SetFailure indicate that operation has failed.
	// The optional category names the cause reported in PSpan.err, defaulting
	// to ErrorCategoryUnknown; only the first one given is used.
	SetFailure(category ...ErrorCategory)

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

	// AppendLong records a long value to annotation.
	AppendLong(key int32, l int64)

	// AppendString records a string value to annotation.
	AppendString(key int32, s string)

	// AppendStringString records two string values to annotation.
	AppendStringString(key int32, s1 string, s2 string)

	// AppendIntStringString records an integer value and two string values to annotation.
	AppendIntStringString(key int32, i int32, s1 string, s2 string)

	// AppendBytesStringString records a copy of an array of byte and two string values to annotation.
	AppendBytesStringString(key int32, b []byte, s1 string, s2 string)

	// AppendLongIntIntByteByteString records a long integer value, two integer value, two byte value and a string value to annotation.
	AppendLongIntIntByteByteString(key int32, l int64, i1 int32, i2 int32, b1 int32, b2 int32, s string)
}

// DistributedTracingContextReader reads distributed tracing headers from carrier.
type DistributedTracingContextReader interface {
	// Get returns the value of a given key from carrier, and whether the
	// carrier holds the key at all.
	//
	// The two are separate answers: a header held with an empty value returns
	// ("", true), and one the carrier does not hold returns ("", false). Trace
	// continuation depends on the difference - a proxy that blanks
	// Pinpoint-SpanID instead of dropping it still describes a hop, and the
	// trace must not split there. A carrier over a source that cannot tell the
	// two apart reports a value it has as present and an empty one as absent
	// (v, v != ""), which is how this agent read every carrier before Get
	// reported presence. See doc/api_contracts.md.
	Get(key string) (string, bool)
}

// HttpHeaderReader adapts a net/http.Header to
// DistributedTracingContextReader. A stdlib type cannot carry the two-result
// Get the interface asks for, and net/http.Header is what a server hands over
// as the inbound carrier, so wrap it here:
//
//	tracer := pinpoint.GetAgent().NewSpanTracerWithReader(
//		"HTTP Server", req.URL.Path, pinpoint.HttpHeaderReader(req.Header))
//
// The http plugin does this for you (NewHttpServerTracer). Presence comes from
// the header map, so a header the client sent empty is reported as present.
func HttpHeaderReader(h http.Header) DistributedTracingContextReader {
	return httpHeaderReader(h)
}

type httpHeaderReader http.Header

func (r httpHeaderReader) Get(key string) (string, bool) {
	// Keys are stored canonicalized, as net/http.Header.Get looks them up.
	if v := r[textproto.CanonicalMIMEHeaderKey(key)]; len(v) > 0 {
		return v[0], true
	}
	return "", false
}

// DistributedTracingContextWriter writes distributed tracing headers to carrier.
type DistributedTracingContextWriter interface {
	// Set sets a given key-value pair to carrier.
	Set(key string, value string)
}

// keys of distributed tracing headers
const (
	HeaderTraceId                    = "Pinpoint-TraceID"
	HeaderSpanId                     = "Pinpoint-SpanID"
	HeaderParentSpanId               = "Pinpoint-pSpanID"
	HeaderSampled                    = "Pinpoint-Sampled"
	HeaderFlags                      = "Pinpoint-Flags"
	HeaderParentApplicationName      = "Pinpoint-pAppName"
	HeaderParentApplicationType      = "Pinpoint-pAppType"
	HeaderParentApplicationNamespace = "Pinpoint-pAppNamespace"
	HeaderParentServiceName          = "Pinpoint-pServiceName"
	HeaderHost                       = "Pinpoint-Host"
)

// TransactionId represents that different RPCs are associated with each other as a single transaction.
type TransactionId struct {
	AgentId   string
	StartTime int64
	Sequence  int64
}

// String returns transaction id string.
func (tid TransactionId) String() string {
	// Assembled by hand rather than with fmt.Sprintf: Inject formats a trace id
	// for every outbound request, and the reflection-based formatter boxes both
	// integers on the way. The numeric half is rendered into a stack array
	// first so the builder can be grown to the exact final length, leaving the
	// returned string as the only allocation.
	var num [48]byte // two int64s at their 20-char widest, plus the separator
	n := strconv.AppendInt(num[:0], tid.StartTime, 10)
	n = append(n, '^')
	n = strconv.AppendInt(n, tid.Sequence, 10)

	var b strings.Builder
	b.Grow(len(tid.AgentId) + 1 + len(n))
	b.WriteString(tid.AgentId)
	b.WriteByte('^')
	b.Write(n)
	return b.String()
}

// UrlStatEntry is the URL statistics record a plugin hands to AddMetric.
// Status is informational: the failed histogram follows the span's failure
// mark, which the HTTP plugin sets from Http.Server.StatusCodeErrors before it
// records the entry, so a plugin that records an entry directly marks an error
// status with SetFailure(ErrorCategoryHttpStatus) itself.
type UrlStatEntry struct {
	Url    string
	Method string
	Status int
}

// service types pre-defined
const (
	ServiceTypeGoApp                 = 1800
	ServiceTypeGoFunction            = 1801
	ServiceTypeGoHttpClient          = 9401
	ServiceTypeAsync                 = 100
	ServiceTypeMysql                 = 2100
	ServiceTypeMysqlExecuteQuery     = 2101
	ServiceTypeMssql                 = 2200
	ServiceTypeMssqlExecuteQuery     = 2201
	ServiceTypeOracle                = 2300
	ServiceTypeOracleExecuteQuery    = 2301
	ServiceTypePgSql                 = 2500
	ServiceTypePgSqlExecuteQuery     = 2501
	ServiceTypeCassandraExecuteQuery = 2601
	ServiceTypeMongo                 = 2650
	ServiceTypeMongoExecuteQuery     = 2651
	ServiceTypeGrpc                  = 9160
	ServiceTypeGrpcServer            = 1130
	ServiceTypeMemcached             = 8050
	ServiceTypeRedis                 = 8203
	ServiceTypeKafkaClient           = 8660
	ServiceTypeHbaseClient           = 8800
	ServiceTypeGoElastic             = 9204
)

// annotation keys pre-defined
const (
	AnnotationArgs0               = -1
	AnnotationApi                 = 12
	AnnotationSqlId               = 20
	AnnotationSqlUid              = 25
	AnnotationHttpUrl             = 40
	AnnotationHttpParam           = 41
	AnnotationHttpCookie          = 45
	AnnotationHttpStatusCode      = 46
	AnnotationHttpRequestHeader   = 47
	AnnotationHttpResponseHeader  = 55
	AnnotationHttpProxyHeader     = 300
	AnnotationKafkaTopic          = 140
	AnnotationKafkaPartition      = 141
	AnnotationKafkaOffset         = 142
	AnnotationMongoJasonData      = 150
	AnnotationMongoCollectionInfo = 151
	AnnotationEsDsl               = 173
	AnnotationHbaseClientParams   = 320
	AnnotationExceptionChainId    = -52
)

const (
	LogTransactionIdKey = "PtxId"
	LogSpanIdKey        = "PspanId"
	Logged              = 1
	NotLogged           = 0
	// MetricURLStat records the span's URL statistics entry (*UrlStatEntry).
	// CAS): once a span holds a real Url, later calls keep it and refresh only
	// the Method and Status. An empty Url counts as "not recorded yet".
	MetricURLStat = "URLStat"
	// force = true) semantics: the Url recorded before is replaced. For a host
	// that has to correct an early, less precise guess with the route it
	// eventually matched. A separate key rather than a field on UrlStatEntry
	// so existing callers and the exported struct are untouched.
	MetricURLStatForce = "URLStatForce"
)
