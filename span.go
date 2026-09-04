package pinpoint

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"slices"
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

type span struct {
	agent *agent
	// cfg is pinned when the span is created and kept for its whole life, so a
	// reload can never move this span's event limits mid-trace.
	cfg                *configSnapshot
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

	// Atomic: a Tracer instruments a single call stack, but plugins cannot
	// always keep one on a single goroutine - a gRPC client stream is driven
	// from whatever goroutines the application chooses, gocql runs observers on
	// speculative-execution goroutines, pgxpool dials on a background one. The
	// event stack has its own lock; these counters ride beside it, and atomics
	// keep such concurrent use a trace-quality problem instead of a data race.
	eventSequence    atomic.Int32
	eventDepth       atomic.Int32
	eventOverflow    atomic.Int32
	eventOverflowLog atomic.Bool
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
	recovered       atomic.Bool
	asyncId         int32
	asyncSequence   int32
	goroutineId     int64
	eventStack      *stack
	urlStat         *UrlStatEntry
	errorChains     []*exception
	errorChainsLock sync.Mutex
	finished        atomic.Bool
}

// generateSpanId is a var so tests can force a collision; production always
// draws from rand.
var generateSpanId = rand.Int63

// nextSpanId draws the id handed to the next node. Java's SpanId.nextSpanID
// guarantees it differs from this span's own id and from its parent's, and is
// never the -1 NULL marker; rand.Int63 makes a collision a 2^-63 event and -1
// impossible, but the downstream link is wrong when it does happen.
func nextSpanId(spanId int64, parentSpanId int64) int64 {
	for {
		if id := generateSpanId(); id != spanId && id != parentSpanId && id != -1 {
			return id
		}
	}
}

// defaultSpan pins the agent's current config snapshot along with the agent
// itself: the two always travel together, and the span keeps that snapshot for
// its whole life.
func defaultSpan(agent *agent) *span {
	span := span{}

	span.agent = agent
	span.cfg = agent.config.load()
	span.parentSpanId = -1
	span.parentAppName = ""
	span.parentAppType = 1 //UNKNOWN
	span.parentServiceName = ""
	span.eventDepth.Store(1)
	span.serviceType = ServiceTypeGoApp
	span.startTime = time.Now()
	span.goroutineId = -1
	span.asyncId = noneAsyncId
	span.eventStack = newStack()
	span.spanEvents = make([]*spanEvent, 0, span.cfg.spanEventChunkSize)
	span.errorChains = make([]*exception, 0)

	return &span
}

func newSampledSpan(agent *agent, operation string, rpcName string) *span {
	span := defaultSpan(agent)

	span.operationName = operation
	span.rpcName = rpcName
	span.apiId = agent.cacheSpanApi(operation, apiTypeWebRequest)

	return span
}

func (span *span) EndSpan() {
	// A second EndSpan would double-count the response time, re-enqueue the
	// url stat and send a second final chunk with the same span id.
	if !span.finished.CompareAndSwap(false, true) {
		Log("span").Warnf("abnormal span - EndSpan already called")
		return
	}

	endTime := time.Now()
	span.elapsed = endTime.UnixMilli() - span.startTime.UnixMilli()

	if span.isAsyncSpan() {
		span.EndSpanEvent() //async span event
	} else {
		dropSampledActiveSpan(span)
		span.agent.stats.collectResponseTime(span.elapsed)
	}

	// Unbalanced end: the leftover events are ended and still recorded, as the
	// C++ agent does. Their sequence numbers were already handed out, so
	// dropping them would send a span whose event sequence has holes and the
	// collector would rebuild the call tree against the missing parents.
	if leftover := span.eventStack.endAll(); len(leftover) > 0 {
		Log("span").Warnf("abnormal span - %d unclosed event(s) ended by EndSpan", len(leftover))
		for _, se := range leftover {
			span.appendEndedSpanEvent(se)
		}
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
		// Failed on an error status or on any recorded error (Java: status = errorCode == 0).
		span.agent.enqueueUrlStat(&urlStat{entry: span.urlStat, endTime: endTime, elapsed: span.elapsed, statusErr: span.statusErr | span.err})
	}
}

func (span *span) Inject(writer DistributedTracingContextWriter) {
	// The trace context is written even when the span has overflowed
	// (spanMaxEventDepth/spanMaxEventSequence exceeded). Overflow limits
	// profiling detail; it is not a sampling decision. Skipping the headers
	// makes the downstream start a new trace and silently cuts the call chain.
	//
	// se is nil while overflowed: the overflowed event was never pushed, so
	// there is nothing to record the link on - peek() would hand back an
	// ancestor event that did not make this call.
	var se *spanEvent
	if span.eventOverflow.Load() == 0 {
		if cur, ok := span.eventStack.peek(); ok {
			se = cur
		} else {
			Log("span").Warnf("abnormal span - has no event")
		}
	}

	writer.Set(HeaderTraceId, span.txId.String())

	// Overflowed: the next span id is generated but not recorded, so the
	// downstream still joins this transaction under this span as its parent.
	// Only the caller-side event->span link is lost, along with the event.
	nextSpanId := nextSpanId(span.spanId, span.parentSpanId)
	if se != nil {
		nextSpanId = se.generateNextSpanId()
	}
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

	destinationId := ""
	if se != nil {
		se.endPoint = se.destinationId
		destinationId = se.destinationId
		writer.Set(HeaderHost, destinationId)
	}

	if IsTraceLogLevelEnabled() {
		Log("span").Tracef("span inject: %v, %d, %d, %s", span.txId, nextSpanId, span.spanId, destinationId)
	}
}

func (span *span) Extract(reader DistributedTracingContextReader) {
	tid := reader.Get(HeaderTraceId)
	if agentId, startTime, sequence, ok := splitTransactionId(tid); ok {
		span.txId.AgentId = agentId
		span.txId.StartTime = startTime
		span.txId.Sequence = sequence
	} else {
		span.txId = span.agent.generateTransactionId()
		if tid != "" {
			// A malformed trace id means the other Pinpoint headers cannot be
			// trusted either: adopting their span/parent ids would record a
			// root span pointing at a parent that does not exist. Start a
			// fresh transaction and ignore every other Pinpoint header.
			Log("span").Warnf("malformed trace id header %q: ignoring pinpoint headers, starting a new transaction", tid)
			span.spanId = generateSpanId()
			span.parentSpanId = -1
			addSampledActiveSpan(span)
			return
		}
	}

	spanid := reader.Get(HeaderSpanId)
	if spanid != "" {
		// bitSize 64, not 0: span ids are int64 and 0 means platform int, so
		// a 32-bit build failed to parse an upstream node's id and silently
		// left the span id at zero, breaking the distributed trace.
		if v, err := strconv.ParseInt(spanid, 10, 64); err == nil {
			span.spanId = v
		} else {
			Log("span").Warnf("malformed span id header %q: generating a new span id", spanid)
			span.spanId = generateSpanId()
		}
	} else {
		span.spanId = generateSpanId()
	}

	pspanid := reader.Get(HeaderParentSpanId)
	if pspanid != "" {
		if v, err := strconv.ParseInt(pspanid, 10, 64); err == nil {
			span.parentSpanId = v
		} else {
			Log("span").Warnf("malformed parent span id header %q: treating span as root", pspanid)
			span.parentSpanId = -1
		}
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

const (
	maxTraceIdAgentIdLength = 24
	maxTraceIdNumberLength  = 20
)

// splitTransactionId parses an "agentId^startTime^sequence" trace id header
// without allocating (no strings.Split slice) and without risking an
// index-out-of-range panic on a malformed or hostile header. It is as strict
// as the Java and C++ agents: exactly three fields, agentId of 1..24 bytes,
// startTime/sequence of 1..20 decimal digits that fit in an int64. ok is false
// otherwise, and the caller starts a new transaction.
func splitTransactionId(tid string) (agentId string, startTime int64, sequence int64, ok bool) {
	i := strings.IndexByte(tid, '^')
	if i < 1 || i > maxTraceIdAgentIdLength {
		return "", 0, 0, false
	}
	rest := tid[i+1:]
	j := strings.IndexByte(rest, '^')
	if j < 0 || strings.IndexByte(rest[j+1:], '^') >= 0 {
		return "", 0, 0, false
	}
	if startTime, ok = parseTraceIdNumber(rest[:j]); !ok {
		return "", 0, 0, false
	}
	if sequence, ok = parseTraceIdNumber(rest[j+1:]); !ok {
		return "", 0, 0, false
	}
	return tid[:i], startTime, sequence, true
}

// parseTraceIdNumber accepts only 1..20 ASCII digits (no sign, no space) that
// fit in an int64.
func parseTraceIdNumber(s string) (int64, bool) {
	if len(s) == 0 || len(s) > maxTraceIdNumberLength {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	return v, err == nil
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

	cfg := span.cfg
	// eventDepth holds the depth the new event would be recorded at (it starts
	// at 1), so depth == max is still the last allowed level - Java's
	// DefaultCallStack overflows at maxDepth < index. Sequence keeps >=,
	// mirroring Java's maxSequence <= sequence.
	if span.eventSequence.Load() >= cfg.spanMaxEventSequence || span.eventDepth.Load() > cfg.spanMaxEventDepth {
		span.eventOverflow.Add(1)
		if span.eventOverflowLog.CompareAndSwap(false, true) {
			Log("span").Warnf("callStack maximum depth/sequence exceeded. (depth=%d, seq=%d)", span.eventDepth.Load(), span.eventSequence.Load())
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
	span.eventSequence.Add(1)
	span.eventDepth.Add(1)
}

func (span *span) EndSpanEvent() {
	if span.eventOverflow.Load() > 0 {
		span.eventOverflow.Add(-1)
		return
	}
	if se, ok := span.eventStack.pop(); ok {
		if !span.recovered.Load() {
			if v := recover(); v != nil {
				err, ok := v.(error)
				if !ok {
					err = errors.New(fmt.Sprint(v))
				}
				// SetError before end(): a finished event drops setters.
				se.SetError(err, "panic")
				span.SetError(err)
				span.recovered.Store(true)
				se.end()
				// Record the event before re-panicking: it was already popped,
				// so skipping the append would drop the very event that
				// captured the panic.
				span.appendEndedSpanEvent(se)
				// Re-panic with the original value, not the recorded error:
				// converting a non-error panic to an error broke every
				// upstream recover comparing against the value it panicked
				// with (a sentinel string, a custom type).
				panic(v)
			}
		}
		se.end()
		span.appendEndedSpanEvent(se)
	} else {
		Log("span").Warnf("abnormal span - has no event")
	}
}

// appendEndedSpanEvent records a completed event, cutting a chunk for the
// sender once enough have accumulated.
func (span *span) appendEndedSpanEvent(se *spanEvent) {
	span.spanEventLock.Lock()
	defer span.spanEventLock.Unlock()

	span.spanEvents = append(span.spanEvents, se)
	if len(span.spanEvents) >= span.cfg.spanEventChunkSize {
		chunk := span.newEventChunk(false)
		if !chunk.enqueue() && IsTraceLogLevelEnabled() {
			Log("span").Tracef("span channel - max capacity reached or closed")
		}
	}
}

func (span *span) newAsyncSpan() Tracer {
	if span.eventOverflow.Load() > 0 {
		return NoopTracer()
	}
	if se, ok := span.eventStack.peek(); ok {
		asyncSpan := defaultSpan(span.agent)

		asyncSpan.cfg = span.cfg // an async span continues under its parent's snapshot
		asyncSpan.txId = span.txId
		asyncSpan.spanId = span.spanId

		// Under spanEventLock: NewGoroutineTracer may be called concurrently
		// from goroutines sharing the parent tracer, and an unsynchronized
		// update here could hand two async spans the same (asyncId, sequence).
		span.spanEventLock.Lock()
		for se.asyncId == noneAsyncId {
			se.asyncId = span.agent.asyncIdGen.Add(1)
		}
		se.asyncSeqGen++
		asyncSpan.asyncId = se.asyncId
		asyncSpan.asyncSequence = se.asyncSeqGen
		span.spanEventLock.Unlock()

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
	if span.eventOverflow.Load() > 0 {
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
	// A call stack overflow only blocks span events; the span level error is
	// still recorded, as the Java agent's DefaultSpanRecorder.recordException does.
	if e == nil || span.finished.Load() {
		return
	}

	errName := errorTypeName(e)
	id := span.agent.cacheError(errName)
	span.errorFuncId = id
	span.errorString = abbreviateString(e.Error(), maxErrorMessageSize)
	// Java IgnoreErrorHandler: a matched error keeps its exception info but
	// does not fail the span.
	if !span.cfg.ignoreError(e, errName) {
		span.err = 1
	}
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
	if span.cfg.collectUrlStat {
		if stat.Url == "" {
			stat.Url = "UNKNOWN_URL"
		}

		span.urlStat = stat
	}
}

func (span *span) AddMetric(metric string, value interface{}) {
	if metric == MetricURLStat {
		if entry, ok := value.(*UrlStatEntry); ok && entry != nil {
			span.collectUrlStat(entry)
		} else {
			Log("span").Warnf("AddMetric: value for %s must be *UrlStatEntry", MetricURLStat)
		}
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

func (span *span) canAddErrorChain() bool {
	return span.errorChains != nil && len(span.errorChains) < maxErrorChainEntry
}

type spanChunk struct {
	span       *span
	eventChunk []*spanEvent
	final      bool
	keyTime    int64
	// endPoint is captured when the chunk is cut: the sender serializes
	// non-final chunks while the span is still live on the request goroutine,
	// so reading span.endPoint there would race with SetEndPoint.
	endPoint string
}

func (span *span) newEventChunk(final bool) *spanChunk {
	// must spanEventLock holder
	chunk := &spanChunk{
		span:       span,
		eventChunk: span.spanEvents,
		final:      final,
		keyTime:    0,
		endPoint:   span.endPoint,
	}

	capacity := span.cfg.spanEventChunkSize
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

	// slices.SortFunc, not sort.Slice: this runs on the request goroutine per
	// chunk, and sort.Slice builds a reflect-based swapper for the slice on
	// every call.
	slices.SortFunc(chunk.eventChunk, func(a, b *spanEvent) int {
		return cmp.Compare(a.sequence, b.sequence)
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

// endAll ends every still-open event, most-recent first to preserve the
// original LIFO close order, and returns them so the caller can still record
// them. Returns nil when the stack is empty.
func (s *stack) endAll() []*spanEvent {
	s.lock.Lock()
	defer s.lock.Unlock()

	if len(s.buf) == 0 {
		return nil
	}
	ended := make([]*spanEvent, 0, len(s.buf))
	for i := len(s.buf) - 1; i >= 0; i-- {
		s.buf[i].end()
		ended = append(ended, s.buf[i])
		s.buf[i] = nil
	}
	s.buf = s.buf[:0]
	return ended
}
