package pinpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	apiTypeDefault         = 0
	apiTypeWebRequest      = 100
	apiTypeInvocation      = 200
	noneAsyncId            = 0
	minEventDepth          = 2
	minEventSequence       = 4
	defaultEventDepth      = 64
	defaultEventSequence   = 5000
	defaultEventChunkSize  = 20
	defaultEventStackDepth = 8
	maxErrorChainEntry     = 10
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
	parentServiceName  string
	serviceType        int32
	rpcName            string
	endPoint           string
	remoteAddr         string
	acceptorHost       string
	annotations        annotation
	loggingInfo        int32
	apiId              int32

	eventSequence    int32
	eventDepth       int32
	eventOverflow    int
	eventOverflowLog bool
	spanEvents       []*spanEvent
	spanEventLock    sync.Mutex

	startTime       time.Time
	elapsed         int64
	operationName   string
	flags           int
	err             int
	statusErr       int
	errorFuncId     int32
	errorString     string
	recovered       bool
	asyncId         int32
	asyncSequence   int32
	goroutineId     int64
	eventStack      *stack
	urlStat         *UrlStatEntry
	errorChains     []*exception
	errorChainsLock sync.Mutex
}

func generateSpanId() int64 {
	return rand.Int63()
}

func defaultSpan() *span {
	span := span{}

	span.parentSpanId = -1
	span.parentAppName = ""
	span.parentAppType = 1 //UNKNOWN
	span.parentServiceName = ""
	span.eventDepth = 1
	span.serviceType = ServiceTypeGoApp
	span.startTime = time.Now()
	span.goroutineId = -1
	span.asyncId = noneAsyncId
	span.eventStack = newStack()
	span.spanEvents = make([]*spanEvent, 0, defaultEventChunkSize)
	span.errorChains = make([]*exception, 0)

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
	span.elapsed = endTime.UnixMilli() - span.startTime.UnixMilli()

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

	span.spanEventLock.Lock()
	defer span.spanEventLock.Unlock()

	chunk := span.newEventChunk(true)
	if chunk.enqueue() {
		if span.errorChains != nil && len(span.errorChains) > 0 {
			span.agent.enqueueExceptionMeta(span)
			span.errorChains = nil
		}
	} else if IsTraceLogLevelEnabled() {
		Log("span").Tracef("span channel - max capacity reached or closed")
	}

	if span.urlStat != nil {
		span.agent.enqueueUrlStat(&urlStat{entry: span.urlStat, endTime: endTime, elapsed: span.elapsed, statusErr: span.statusErr})
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

		// Propagate this agent's serviceName so downstream records it as the
		// parent serviceName. Only set when present (v4), matching the Java
		// agent's "serviceName != NOT_SET" guard; v1/v3 emit no such header.
		if span.agent.serviceName != "" {
			writer.Set(HeaderParentServiceName, span.agent.serviceName)
		}

		se.endPoint = se.destinationId
		writer.Set(HeaderHost, se.destinationId)

		if IsTraceLogLevelEnabled() {
			Log("span").Tracef("span inject: %v, %d, %d, %s", span.txId, nextSpanId, span.spanId, se.destinationId)
		}
	} else {
		Log("span").Warnf("abnormal span - has no event")
	}
}

func (span *span) Extract(reader DistributedTracingContextReader) {
	tid := reader.Get(HeaderTraceId)
	if agentId, startTime, sequence, ok := splitTransactionId(tid); ok {
		span.txId.AgentId = agentId
		span.txId.StartTime = startTime
		span.txId.Sequence = sequence
	} else {
		if tid != "" {
			Log("span").Warnf("malformed trace id header: %q", tid)
		}
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

	pservicename := reader.Get(HeaderParentServiceName)
	if pservicename != "" {
		span.parentServiceName = pservicename
	}

	host := reader.Get(HeaderHost)
	if host != "" {
		span.acceptorHost = host
		span.endPoint = host
		span.remoteAddr = host // for message queue (kafka, ...)
	}

	addSampledActiveSpan(span)
	if IsTraceLogLevelEnabled() {
		Log("span").Tracef("span extract: %s, %s, %s, %s, %s, %s", tid, spanid, pappname, pspanid, papptype, host)
	}
}

// splitTransactionId parses an "agentId^startTime^sequence" trace id header
// without allocating (no strings.Split slice) and without risking an
// index-out-of-range panic on a malformed or hostile header. ok is false when
// the structure is missing the two separators, in which case the caller starts
// a new transaction. Numeric parse errors in the time/sequence fields are
// tolerated (left as 0), matching the previous behavior.
func splitTransactionId(tid string) (agentId string, startTime int64, sequence int64, ok bool) {
	if tid == "" {
		return "", 0, 0, false
	}
	i := strings.IndexByte(tid, '^')
	if i < 0 {
		return "", 0, 0, false
	}
	rest := tid[i+1:]
	j := strings.IndexByte(rest, '^')
	if j < 0 {
		return "", 0, 0, false
	}
	agentId = tid[:i]
	startTime, _ = strconv.ParseInt(rest[:j], 10, 64)
	sequence, _ = strconv.ParseInt(rest[j+1:], 10, 64)
	return agentId, startTime, sequence, true
}

func (span *span) NewSpanEvent(operationName string) Tracer {
	if IsDebugLogLevelEnabled() {
		if goIdOffset > 0 {
			if span.goroutineId < 0 {
				span.goroutineId = goIdFromG()
			} else if span.goroutineId != goIdFromG() {
				Log("span").Warnf("span is shared by more than two goroutines.")
				return span
			}
		}
	}

	cfg := span.config()
	if span.eventSequence >= cfg.spanMaxEventSequence || span.eventDepth >= cfg.spanMaxEventDepth {
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
	span.spanEventLock.Lock()
	defer span.spanEventLock.Unlock()

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

		span.spanEventLock.Lock()
		defer span.spanEventLock.Unlock()

		span.spanEvents = append(span.spanEvents, se)
		if len(span.spanEvents) >= span.config().spanEventChunkSize {
			chunk := span.newEventChunk(false)
			if !chunk.enqueue() && IsTraceLogLevelEnabled() {
				Log("span").Tracef("span channel - max capacity reached or closed")
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

func (span *span) AsyncSpanId() string {
	return fmt.Sprintf("%d^%d^%d", span.spanId, span.asyncId, span.asyncSequence)
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
	span.statusErr = 1
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
	if span.config().collectUrlStat {
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
	m["Annotations"] = span.annotations.getList()
	b, _ := json.Marshal(m)
	return b
}

func (span *span) config() *Config {
	return span.agent.config
}

func (span *span) canAddErrorChain() bool {
	return span.errorChains != nil && len(span.errorChains) < maxErrorChainEntry
}

type spanChunk struct {
	span       *span
	eventChunk []*spanEvent
	final      bool
	keyTime    int64
}

func (span *span) newEventChunk(final bool) *spanChunk {
	// must spanEventLock holder
	chunk := &spanChunk{
		span:       span,
		eventChunk: span.spanEvents,
		final:      final,
		keyTime:    0,
	}

	capacity := defaultEventChunkSize
	if final {
		capacity = 0
	}
	span.spanEvents = make([]*spanEvent, 0, capacity)
	return chunk
}

func (chunk *spanChunk) enqueue() bool {
	chunk.optimizeSpanEvents()
	return chunk.span.agent.enqueueSpan(chunk)
}

func (chunk *spanChunk) optimizeSpanEvents() {
	var prevSe *spanEvent
	var prevDepth int32

	if len(chunk.eventChunk) < 1 {
		return
	}

	sort.Slice(chunk.eventChunk, func(i, j int) bool {
		return chunk.eventChunk[i].sequence < chunk.eventChunk[j].sequence
	})
	if chunk.final {
		chunk.keyTime = chunk.span.startTime.UnixMilli()
	} else {
		chunk.keyTime = chunk.eventChunk[0].startTime
	}

	for i, se := range chunk.eventChunk {
		if i == 0 {
			se.startElapsed = se.startTime - chunk.keyTime
		} else {
			se.startElapsed = se.startTime - prevSe.startTime
			curDepth := se.depth
			if prevDepth == curDepth {
				se.depth = 0
			}
			prevDepth = curDepth
		}
		prevSe = se
	}
}

// stack is the LIFO of currently-open span events. It is backed by a slice
// (not a linked list) so that pushing an event reuses the preallocated backing
// array instead of allocating a node per call on the hot path.
type stack struct {
	lock sync.Mutex
	buf  []*spanEvent
}

func newStack() *stack {
	return &stack{buf: make([]*spanEvent, 0, defaultEventStackDepth)}
}

func (s *stack) len() int {
	s.lock.Lock()
	defer s.lock.Unlock()
	return len(s.buf)
}

func (s *stack) push(v *spanEvent) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.buf = append(s.buf, v)
}

func (s *stack) pop() (*spanEvent, bool) {
	s.lock.Lock()
	defer s.lock.Unlock()

	n := len(s.buf)
	if n > 0 {
		save := s.buf[n-1]
		s.buf[n-1] = nil // don't retain the popped event
		s.buf = s.buf[:n-1]
		return save, true
	}
	return nil, false
}

func (s *stack) peek() (*spanEvent, bool) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if n := len(s.buf); n > 0 {
		return s.buf[n-1], true
	}
	return nil, false
}

func (s *stack) empty() {
	s.lock.Lock()
	defer s.lock.Unlock()

	// end most-recent first to preserve the original LIFO close order
	for i := len(s.buf) - 1; i >= 0; i-- {
		s.buf[i].end()
		s.buf[i] = nil
	}
	s.buf = s.buf[:0]
}
