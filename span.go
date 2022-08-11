package pinpoint

import (
	"container/list"
	"context"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var asyncIdGen int32 = 0

type span struct {
	agent              Agent
	txId               TransactionId
	spanId             int64
	parentSpanId       int64
	parentAppName      string
	parentAppType      int
	parentAppNamespace string
	serviceType        int32
	rpcName            string
	endPoint           string
	remoteAddr         string
	acceptorHost       string
	spanEvents         []*spanEvent
	annotations        annotation
	loggingInfo        int32
	apiId              int32

	eventSequence int32
	eventDepth    int32

	startTime     time.Time
	duration      time.Duration
	operationName string
	flags         int
	err           int
	errorFuncId   int32
	errorString   string
	asyncId       int32
	asyncSequence int32
	goroutineId   uint64
	stack         *list.List
}

func toMicroseconds(d time.Duration) int64 { return int64(d) / 1e3 }

func toMilliseconds(d time.Duration) int64 { return int64(d) / 1e6 }

func generateSpanId() int64 {
	return rand.Int63()
}

func defaultSpan() *span {
	span := span{}

	span.parentSpanId = -1
	span.parentAppType = -1
	span.eventDepth = 1
	span.serviceType = ServiceTypeGoApp
	span.startTime = time.Now()
	span.goroutineId = 0

	span.stack = list.New()
	return &span
}

func newSampledSpan(agent Agent, operation string, rpcName string) Tracer {
	span := defaultSpan()

	span.agent = agent
	span.operationName = operation
	span.rpcName = rpcName
	span.apiId = agent.RegisterSpanApiId(operation, ApiTypeWebRequest)

	return span
}

func (span *span) EndSpan() {
	for e := span.stack.Front(); e != nil; e = e.Next() {
		se, ok := e.Value.(*spanEvent)
		if ok {
			se.end()
		}
	}

	dropSampledActiveSpan(span)

	span.duration = time.Now().Sub(span.startTime)
	collectResponseTime(toMilliseconds(span.duration))

	if !span.agent.TryEnqueueSpan(span) {
		log("span").Debug("span channel - max capacity reached or closed")
	}
}

func (span *span) Inject(writer DistributedTracingContextWriter) {
	writer.Set(HttpTraceId, span.txId.String())

	se := span.stack.Front().Value.(*spanEvent)
	nextSpanId := se.generateNextSpanId()
	writer.Set(HttpSpanId, strconv.FormatInt(nextSpanId, 10))

	writer.Set(HttpParentSpanId, strconv.FormatInt(span.spanId, 10))
	writer.Set(HttpFlags, strconv.Itoa(span.flags))
	writer.Set(HttpParentApplicationName, span.agent.Config().ApplicationName)
	writer.Set(HttpParentApplicationType, strconv.Itoa(int(span.agent.Config().ApplicationType)))
	writer.Set(HttpParentApplicationNamespace, "")

	se.endPoint = se.destinationId
	writer.Set(HttpHost, se.destinationId)

	log("span").Debug("span inject: ", span.txId, nextSpanId, span.spanId, se.destinationId)
}

func (span *span) Extract(reader DistributedTracingContextReader) {
	tid := reader.Get(HttpTraceId)
	if tid != "" {
		s := strings.Split(tid, "^")
		span.txId.AgentId = s[0]
		span.txId.StartTime, _ = strconv.ParseInt(s[1], 10, 0)
		span.txId.Sequence, _ = strconv.ParseInt(s[2], 10, 0)
	} else {
		span.txId = span.agent.GenerateTransactionId()
	}

	spanid := reader.Get(HttpSpanId)
	if spanid != "" {
		span.spanId, _ = strconv.ParseInt(spanid, 10, 0)
	} else {
		span.spanId = generateSpanId()
	}

	pspanid := reader.Get(HttpParentSpanId)
	if pspanid != "" {
		span.parentSpanId, _ = strconv.ParseInt(pspanid, 10, 0)
	}

	flag := reader.Get(HttpFlags)
	if flag != "" {
		span.flags, _ = strconv.Atoi(flag)
	}

	pappname := reader.Get(HttpParentApplicationName)
	if pappname != "" {
		span.parentAppName = pappname
	}

	papptype := reader.Get(HttpParentApplicationType)
	if papptype != "" {
		span.parentAppType, _ = strconv.Atoi(papptype)
	}

	host := reader.Get(HttpHost)
	if host != "" {
		span.acceptorHost = host
		span.remoteAddr = host
		span.endPoint = host
	}

	addSampledActiveSpan(span)
	log("span").Debug("span extract: ", tid, spanid, pappname, pspanid, papptype, host)
}

func (span *span) NewSpanEvent(operationName string) Tracer {
	se := newSpanEvent(span, operationName)
	span.eventSequence++
	span.eventDepth++

	span.spanEvents = append(span.spanEvents, se)
	span.stack.PushFront(se)

	return span
}

func (span *span) EndSpanEvent() {
	if span.stack.Len() > 0 {
		e := span.stack.Front()
		span.stack.Remove(e).(*spanEvent).end()
	}
}

func (span *span) newAsyncSpan() Tracer {
	se := span.stack.Front().Value.(*spanEvent)
	asyncSpan := newSpanForAsync(span)

	if se.asyncId == 0 {
		se.asyncId = atomic.AddInt32(&asyncIdGen, 1)
	}
	asyncSpan.asyncId = se.asyncId

	se.asyncSeqGen++
	asyncSpan.asyncSequence = se.asyncSeqGen
	asyncSpan.newSpanEventForAsync()

	return asyncSpan
}

func newSpanForAsync(parentSpan *span) *span {
	span := defaultSpan()

	span.agent = parentSpan.agent
	span.txId = parentSpan.txId
	span.spanId = parentSpan.spanId

	return span
}

func (span *span) newSpanEventForAsync() {
	se := newSpanEventGoroutine(span)

	span.eventSequence++
	span.eventDepth++

	span.spanEvents = append(span.spanEvents, se)
	span.stack.PushFront(se)
}

//Deprecated
func (span *span) NewAsyncSpan() Tracer {
	return span.newAsyncSpan()
}

func (span *span) NewGoroutineTracer() Tracer {
	return span.newAsyncSpan()
}

func (span *span) WrapGoroutine(goroutineName string, goroutine func(context.Context), ctx context.Context) func() {
	asyncSpan := span.newAsyncSpan()

	var newCtx context.Context
	if ctx == nil {
		newCtx = NewContext(context.Background(), asyncSpan)
	} else {
		newCtx = NewContext(ctx, asyncSpan)
	}

	return func() {
		defer asyncSpan.EndSpan()
		defer asyncSpan.NewSpanEvent(goroutineName).EndSpanEvent()
		goroutine(newCtx)
	}
}

func (span *span) TransactionId() TransactionId {
	return span.txId
}

func (span *span) SpanId() int64 {
	return span.spanId
}

func (span *span) Span() SpanRecorder {
	return span
}

func (span *span) SpanEvent() SpanEventRecorder {
	return span.stack.Front().Value.(*spanEvent)
}

func (span *span) SetError(e error) {
	if e == nil {
		return
	}

	id := span.agent.CacheErrorFunc(span.operationName)
	span.errorFuncId = id
	span.errorString = e.Error()
	span.err = 1
}

func (span *span) SetApiId(id int32) {
	span.apiId = id
}

func (span *span) SetServiceType(typ int32) {
	span.serviceType = typ
}

func (span *span) SetRpcName(rpc string) {
	span.rpcName = rpc
}

func (span *span) SetRemoteAddress(remoteAddress string) {
	span.remoteAddr = remoteAddress
}

func (span *span) SetEndPoint(endPoint string) {
	span.endPoint = endPoint
}

func (span *span) SetAcceptorHost(host string) {
	span.acceptorHost = host
}

func (span *span) Annotations() Annotation {
	return &span.annotations
}

func (span *span) SetLogging(logInfo int32) {
	span.loggingInfo = logInfo
}

func (span *span) RecordHttpStatus(status int) {
	span.annotations.AppendInt(AnnotationHttpStatusCode, int32(status))

	if span.err == 0 && span.agent.IsHttpError(status) {
		span.err = 1
	}
}

func (span *span) RecordHttpHeader(annotation Annotation, key int, header http.Header) {
	span.agent.HttpHeaderRecorder(key).recordHeader(annotation, key, header)
}

func (span *span) RecordHttpCookie(annotation Annotation, cookie []*http.Cookie) {
	span.agent.HttpHeaderRecorder(AnnotationHttpCookie).recordCookie(annotation, cookie)
}
