package pinpoint

import (
	"context"
	"fmt"
	"time"
)

type TransactionId struct {
	AgentId   string
	StartTime int64
	Sequence  int64
}

func (tid TransactionId) String() string {
	return fmt.Sprintf("%s^%d^%d", tid.AgentId, tid.StartTime, tid.Sequence)
}

type Agent interface {
	Shutdown()
	NewSpanTracer(operation string, rpcName string) Tracer
	NewSpanTracerWithReader(operation string, rpcName string, reader DistributedTracingContextReader) Tracer
	Enable() bool
	ApplicationName() string
	ApplicationType() int32
	AgentID() string
	StartTime() int64
	generateTransactionId() TransactionId
	enqueueSpan(span *span) bool
	cacheErrorFunc(funcName string) int32
	cacheSql(sql string) int32
	cacheSpanApiId(descriptor string, apiType int) int32
}

type Tracer interface {
	NewSpanEvent(operationName string) Tracer
	NewAsyncSpan() Tracer
	NewGoroutineTracer() Tracer
	WrapGoroutine(goroutineName string, goroutine func(context.Context), ctx context.Context) func()
	EndSpan()
	EndSpanEvent()

	Inject(writer DistributedTracingContextWriter)
	Extract(reader DistributedTracingContextReader)

	TransactionId() TransactionId
	SpanId() int64

	Span() SpanRecorder
	SpanEvent() SpanEventRecorder

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
	SamplingTypeCounter      = "COUNTER"
	SamplingTypePercent      = "PERCENT"
	SmaplingMaxPercentRate   = 100 * 100
)
