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

const NoneAsyncId = 0

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
	elapsed       time.Duration
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
	span.asyncId = NoneAsyncId

	span.stack = list.New()
	return &span
}

func newSampledSpan(agent Agent, operation string, rpcName string) Tracer {
	span := defaultSpan()

	span.agent = agent
	span.operationName = operation
	span.rpcName = rpcName
	span.apiId = agent.cacheSpanApiId(operation, ApiTypeWebRequest)

	addSampledActiveSpan(span)

	return span
}

func (span *span) EndSpan() {
	span.elapsed = time.Now().Sub(span.startTime)

	if span.isAsyncSpan() {
		span.EndSpanEvent() //async span event
	} else {
		dropSampledActiveSpan(span)
		collectResponseTime(toMilliseconds(span.elapsed))
	}

	if span.stack.Len() > 0 {
		for e := span.stack.Front(); e != nil; e = e.Next() {
			if se, ok := e.Value.(*spanEvent); ok {
				se.end()
			}
		}
		log("span").Warn("span has a event that is not closed")
	}

	if !span.agent.enqueueSpan(span) {
		log("span").Debug("span channel - max capacity reached or closed")
	}
}

func (span *span) Inject(writer DistributedTracingContextWriter) {
	if se := span.currentSpanEvent(); se != nil {
		writer.Set(HttpTraceId, span.txId.String())

		nextSpanId := se.generateNextSpanId()
		writer.Set(HttpSpanId, strconv.FormatInt(nextSpanId, 10))

		writer.Set(HttpParentSpanId, strconv.FormatInt(span.spanId, 10))
		writer.Set(HttpFlags, strconv.Itoa(span.flags))
		writer.Set(HttpParentApplicationName, ConfigString(cfgAppName))
		writer.Set(HttpParentApplicationType, strconv.Itoa(ConfigInt(cfgAppType)))
		writer.Set(HttpParentApplicationNamespace, "")

		se.endPoint = se.destinationId
		writer.Set(HttpHost, se.destinationId)

		log("span").Debug("span inject: ", span.txId, nextSpanId, span.spanId, se.destinationId)
	} else {
		log("span").Warn("span event is not exist to inject")
	}
}

func (span *span) Extract(reader DistributedTracingContextReader) {
	tid := reader.Get(HttpTraceId)
	if tid != "" {
		s := strings.Split(tid, "^")
		span.txId.AgentId = s[0]
		span.txId.StartTime, _ = strconv.ParseInt(s[1], 10, 0)
		span.txId.Sequence, _ = strconv.ParseInt(s[2], 10, 0)
	} else {
		span.txId = span.agent.generateTransactionId()
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

	log("span").Debug("span extract: ", tid, spanid, pappname, pspanid, papptype, host)
}

func (span *span) NewSpanEvent(operationName string) Tracer {
	span.appendSpanEvent(newSpanEvent(span, operationName))
	return span
}

func (span *span) appendSpanEvent(se *spanEvent) {
	span.spanEvents = append(span.spanEvents, se)
	span.stack.PushFront(se)
	span.eventSequence++
	span.eventDepth++
}

func (span *span) EndSpanEvent() {
	if e := span.stack.Front(); e != nil {
		v := span.stack.Remove(e)
		if se, ok := v.(*spanEvent); ok {
			se.end()
		}
	} else {
		log("span").Warn("span event is not exist to be closed")
	}
}

func (span *span) newAsyncSpan() Tracer {
	if se := span.currentSpanEvent(); se != nil {
		asyncSpan := defaultSpan()

		asyncSpan.agent = span.agent
		asyncSpan.txId = span.txId
		asyncSpan.spanId = span.spanId

		for se.asyncId == NoneAsyncId {
			se.asyncId = atomic.AddInt32(&asyncIdGen, 1)
		}
		se.asyncSeqGen++

		asyncSpan.asyncId = se.asyncId
		asyncSpan.asyncSequence = se.asyncSeqGen
		asyncSpan.appendSpanEvent(newSpanEventGoroutine(asyncSpan))

		return asyncSpan
	} else {
		log("span").Warn("span event is not exist to make async span")
		return NoopTracer()
	}
}

func (span *span) isAsyncSpan() bool {
	return span.asyncId != NoneAsyncId
}

// Deprecated
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
	if se := span.currentSpanEvent(); se != nil {
		return se
	}

	log("span").Warn("span has no event")
	return &defaultNoopSpanEvent
}

func (span *span) currentSpanEvent() *spanEvent {
	if e := span.stack.Front(); e != nil {
		if se, ok := e.Value.(*spanEvent); ok {
			return se
		}
	}
	return nil
}

func (span *span) SetError(e error) {
	if e == nil {
		return
	}

	id := span.agent.cacheErrorFunc("error")
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

	if span.err == 0 && span.agent.isHttpError(status) {
		span.err = 1
	}
}

func (span *span) RecordHttpHeader(annotation Annotation, key int, header http.Header) {
	span.agent.httpHeaderRecorder(key).recordHeader(annotation, key, header)
}

func (span *span) RecordHttpCookie(annotation Annotation, cookie []*http.Cookie) {
	span.agent.httpHeaderRecorder(AnnotationHttpCookie).recordCookie(annotation, cookie)
}

func (span *span) IsSampled() bool {
	return true
}
