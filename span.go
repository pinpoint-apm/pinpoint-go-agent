package pinpoint

import (
	"container/list"
	"math/rand"
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
	sampled       bool
	flags         int
	err           int
	asyncId       int32
	asyncSequence int32
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
	span.sampled = true
	span.serviceType = ServiceTypeGoApp
	span.startTime = time.Now()

	span.stack = list.New()
	return &span
}

func newSampledSpan(agent Agent, operation string) Tracer {
	span := defaultSpan()

	span.agent = agent
	span.operationName = operation

	return span
}

func (span *span) EndSpan() {
	for e := span.stack.Front(); e != nil; e = e.Next() {
		se := e.Value.(*spanEvent)
		se.end()
	}

	dropActiveSpan(span.spanId)

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

	sampled := reader.Get(HttpSampled)
	if sampled == "s0" {
		span.sampled = false
	} else {
		span.sampled = true
	}

	addActiveSpan(span.spanId, span.startTime)
	log("span").Debug("span extract: ", tid, spanid, pappname, pspanid, papptype, host, sampled)
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

func (span *span) NewAsyncSpan() Tracer {
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
	se := newSpanEvent(span, "")
	se.serviceType = 100 // ASYNC
	se.apiId = asyncApiId

	span.eventSequence++
	span.eventDepth++

	span.spanEvents = append(span.spanEvents, se)
	span.stack.PushFront(se)
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
	//se := span.stack.Front().Value.(*spanEvent)
	//se.SetError(e)
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
