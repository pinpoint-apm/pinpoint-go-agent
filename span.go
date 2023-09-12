package pinpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	apiTypeDefault       = 0
	apiTypeWebRequest    = 100
	apiTypeInvocation    = 200
	noneAsyncId          = 0
	minEventDepth        = 2
	minEventSequence     = 4
	defaultEventDepth    = 64
	defaultEventSequence = 5000
)

var (
	asyncIdGen int32 = 0
)

type span struct {
	agent              *agent
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

	eventSequence    int32
	eventDepth       int32
	eventOverflow    int
	eventOverflowLog bool

	startTime     time.Time
	elapsed       int64
	operationName string
	flags         int
	err           int
	errorFuncId   int32
	errorString   string
	recovered     bool
	asyncId       int32
	asyncSequence int32
	goroutineId   int64
	eventStack    *stack
	appendLock    sync.Mutex
	urlStat       *UrlStatEntry
}

func generateSpanId() int64 {
	return rand.Int63()
}

func defaultSpan() *span {
	span := span{}

	span.parentSpanId = -1
	span.parentAppName = ""
	span.parentAppType = 1 //UNKNOWN
	span.eventDepth = 1
	span.serviceType = ServiceTypeGoApp
	span.startTime = time.Now()
	span.goroutineId = 0
	span.asyncId = noneAsyncId
	span.eventStack = &stack{}
	return &span
}

func newSampledSpan(agent *agent, operation string, rpcName string) *span {
	span := defaultSpan()

	span.agent = agent
	span.operationName = operation
	span.rpcName = rpcName
	span.apiId = agent.cacheSpanApi(operation, apiTypeWebRequest)

	return span
}

func (span *span) EndSpan() {
	endTime := time.Now()
	span.elapsed = endTime.Sub(span.startTime).Milliseconds()

	if span.isAsyncSpan() {
		span.EndSpanEvent() //async span event
	} else {
		dropSampledActiveSpan(span)
		collectResponseTime(span.elapsed)
	}

	if span.eventStack.len() > 0 {
		span.eventStack.empty()
		Log("span").Warnf("abnormal span - has unclosed event")
	}

	if !span.agent.enqueueSpan(span) {
		Log("span").Tracef("span channel - max capacity reached or closed")
	}

	if span.urlStat != nil {
		span.agent.enqueueUrlStat(&urlStat{entry: span.urlStat, endTime: endTime, elapsed: span.elapsed})
	}
}

func (span *span) Inject(writer DistributedTracingContextWriter) {
	if span.eventOverflow > 0 {
		return
	}
	if se, ok := span.eventStack.peek(); ok {
		writer.Set(HeaderTraceId, span.txId.String())

		nextSpanId := se.generateNextSpanId()
		writer.Set(HeaderSpanId, strconv.FormatInt(nextSpanId, 10))

		writer.Set(HeaderParentSpanId, strconv.FormatInt(span.spanId, 10))
		writer.Set(HeaderFlags, strconv.Itoa(span.flags))
		writer.Set(HeaderParentApplicationName, span.agent.appName)
		writer.Set(HeaderParentApplicationType, strconv.Itoa(int(span.agent.appType)))
		writer.Set(HeaderParentApplicationNamespace, "")

		se.endPoint = se.destinationId
		writer.Set(HeaderHost, se.destinationId)

		Log("span").Tracef("span inject: %v, %d, %d, %s", span.txId, nextSpanId, span.spanId, se.destinationId)
	} else {
		Log("span").Warnf("abnormal span - has no event")
	}
}

func (span *span) Extract(reader DistributedTracingContextReader) {
	tid := reader.Get(HeaderTraceId)
	if tid != "" {
		s := strings.Split(tid, "^")
		span.txId.AgentId = s[0]
		span.txId.StartTime, _ = strconv.ParseInt(s[1], 10, 0)
		span.txId.Sequence, _ = strconv.ParseInt(s[2], 10, 0)
	} else {
		span.txId = span.agent.generateTransactionId()
	}

	spanid := reader.Get(HeaderSpanId)
	if spanid != "" {
		span.spanId, _ = strconv.ParseInt(spanid, 10, 0)
	} else {
		span.spanId = generateSpanId()
	}

	pspanid := reader.Get(HeaderParentSpanId)
	if pspanid != "" {
		span.parentSpanId, _ = strconv.ParseInt(pspanid, 10, 0)
	}

	flag := reader.Get(HeaderFlags)
	if flag != "" {
		span.flags, _ = strconv.Atoi(flag)
	}

	pappname := reader.Get(HeaderParentApplicationName)
	if pappname != "" {
		span.parentAppName = pappname
	}

	papptype := reader.Get(HeaderParentApplicationType)
	if papptype != "" {
		span.parentAppType, _ = strconv.Atoi(papptype)
	}

	host := reader.Get(HeaderHost)
	if host != "" {
		span.acceptorHost = host
		span.endPoint = host
		span.remoteAddr = host // for message queue (kafka, ...)
	}

	addSampledActiveSpan(span)
	Log("span").Tracef("span extract: %s, %s, %s, %s, %s, %s", tid, spanid, pappname, pspanid, papptype, host)
}

func (span *span) NewSpanEvent(operationName string) Tracer {
	if span.eventSequence >= span.agent.config.spanMaxEventSequence || span.eventDepth >= span.agent.config.spanMaxEventDepth {
		span.eventOverflow++
		if !span.eventOverflowLog {
			Log("span").Warnf("callStack maximum depth/sequence exceeded. (depth=%d, seq=%d)", span.eventDepth, span.eventSequence)
			span.eventOverflowLog = true
		}
	} else {
		span.appendSpanEvent(newSpanEvent(span, operationName))
	}
	return span
}

func (span *span) appendSpanEvent(se *spanEvent) {
	span.appendLock.Lock()
	defer span.appendLock.Unlock()

	span.spanEvents = append(span.spanEvents, se)
	span.eventStack.push(se)
	span.eventSequence++
	span.eventDepth++
}

func (span *span) EndSpanEvent() {
	if span.eventOverflow > 0 {
		span.eventOverflow--
		return
	}
	if se, ok := span.eventStack.pop(); ok {
		se.end()
		if !span.recovered {
			if v := recover(); v != nil {
				err, ok := v.(error)
				if !ok {
					err = errors.New(fmt.Sprint(v))
				}
				se.SetError(err, "panic")
				span.SetError(err)
				span.recovered = true
				panic(err)
			}
		}
	} else {
		Log("span").Warnf("abnormal span - has no event")
	}
}

func (span *span) newAsyncSpan() Tracer {
	if span.eventOverflow > 0 {
		return NoopTracer()
	}
	if se, ok := span.eventStack.peek(); ok {
		asyncSpan := defaultSpan()

		asyncSpan.agent = span.agent
		asyncSpan.txId = span.txId
		asyncSpan.spanId = span.spanId

		for se.asyncId == noneAsyncId {
			se.asyncId = atomic.AddInt32(&asyncIdGen, 1)
		}
		se.asyncSeqGen++

		asyncSpan.asyncId = se.asyncId
		asyncSpan.asyncSequence = se.asyncSeqGen
		asyncSpan.appendSpanEvent(newSpanEventGoroutine(asyncSpan))

		return asyncSpan
	} else {
		Log("span").Warnf("abnormal span - has no event")
		return NoopTracer()
	}
}

func (span *span) isAsyncSpan() bool {
	return span.asyncId != noneAsyncId
}

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
	if span.eventOverflow > 0 {
		return &defaultNoopSpanEvent
	}
	if se, ok := span.eventStack.peek(); ok {
		return se
	}
	Log("span").Warnf("abnormal span - has no event")
	return &defaultNoopSpanEvent
}

func (span *span) IsSampled() bool {
	return true
}

func (span *span) SetError(e error) {
	if e == nil || span.eventOverflow > 0 {
		return
	}

	id := span.agent.cacheError("error")
	span.errorFuncId = id
	span.errorString = e.Error()
	span.err = 1
}

func (span *span) SetFailure() {
	span.err = 1
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

func (span *span) collectUrlStat(stat *UrlStatEntry) {
	if span.agent.config.collectUrlStat {
		if stat.Url == "" {
			stat.Url = "UNKNOWN_URL"
		}

		span.urlStat = stat
	}
}

func (span *span) AddMetric(metric string, value interface{}) {
	if metric == MetricURLStat {
		span.collectUrlStat(value.(*UrlStatEntry))
	}
}

func (span *span) JsonString() []byte {
	m := make(map[string]interface{}, 0)
	m["RpcName"] = span.rpcName
	m["EndPoint"] = span.endPoint
	m["RemoteAddr"] = span.remoteAddr
	m["Err"] = span.err
	m["Events"] = span.spanEvents
	m["Annotations"] = span.annotations.list
	b, _ := json.Marshal(m)
	return b
}

type stack struct {
	lock sync.RWMutex
	top  *node
	size int
}

type node struct {
	value *spanEvent
	next  *node
}

func (s *stack) len() int {
	s.lock.RLock()
	defer s.lock.RUnlock()
	return s.size
}

func (s *stack) push(v *spanEvent) {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.top = &node{value: v, next: s.top}
	s.size++
}

func (s *stack) pop() (*spanEvent, bool) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if s.size > 0 {
		save := s.top.value
		s.top = s.top.next
		s.size--
		return save, true
	}
	return nil, false
}

func (s *stack) peek() (*spanEvent, bool) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	if s.size > 0 {
		return s.top.value, true
	}
	return nil, false
}

func (s *stack) empty() {
	s.lock.Lock()
	defer s.lock.Unlock()

	for s.size > 0 {
		s.top.value.end()
		s.top = s.top.next
		s.size--
	}
}
