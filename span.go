package pinpoint

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime/debug"
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
	noneSpanId             = -1
	unknownAddress         = "UNKNOWN"
	minEventDepth          = 2
	minEventSequence       = 4
	defaultEventDepth      = 64
	defaultEventSequence   = 5000
	defaultEventChunkSize  = 20
	defaultEventStackDepth = 8
	// minErrorChainEntry is the floor on the exception entries a span keeps.
	// canAddErrorChain raises it to Error.MaxChainDepth so a single chain can
	// always be recorded in full: a lower bound would drop links the option
	// promised, and that alone is the reason for the floor. The cap is
	// therefore derived from the option's own clamp ceiling, not chosen.
	minErrorChainEntry = 10
)

// overflowSpanEvent is the recorder handed out while the call stack has
// overflowed. It drops everything a real event would record except the
// destination, which Inject still writes as Pinpoint-Host: overflow is a
// profiling limit, not a reason to hide the caller from the node it calls.
//
// Atomic for the reason the counters beside it are (see span): a plugin may
// record the destination from another goroutine of the same call stack.
type overflowSpanEvent struct {
	noopSpanEvent
	parent        *span
	destinationId atomic.Value // string
}

func (se *overflowSpanEvent) SetDestination(id string) {
	se.destinationId.Store(id)
}

// SetError records nothing on the dropped event - no error string, no
// annotation, no exception chain - but the failure still reaches the span, so
// the transaction is not reported as a success because it failed past the
// recorder during overflow and its recordException marks the trace root with
// DisabledSpanEvent::SetError does the same with markSpanError.
// The Span.IgnoreErrors filter applies exactly as on a recorded event.
func (se *overflowSpanEvent) SetError(e error, errorName ...string) {
	span := se.parent
	if e == nil || span.finished.Load() {
		return
	}
	errName := errorTypeName(e)
	if len(errorName) > 0 {
		errName = errorName[0]
	}
	if !span.cfg.ignoreError(e, errName) {
		span.markSpanError(ErrorCategoryException)
	}
}

// destination reports the last destination recorded during this overflow.
// Nesting is not tracked: a plugin records the destination and injects in the
// same breath, so the last one seen is the call being made.
func (se *overflowSpanEvent) destination() string {
	id, _ := se.destinationId.Load().(string)
	return id
}

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
	// overflowSe stands in for the dropped events while the span is
	// overflowed, keeping the one thing Inject still needs from them.
	overflowSe overflowSpanEvent
	// sqlCount counts the executed queries behind SQL.ErrorCount, and is
	// atomic for the same reason: a plugin may run them from several
	// goroutines of one call stack.
	sqlCount      atomic.Int32
	spanEvents    []*spanEvent
	spanEventLock sync.Mutex

	startTime     time.Time
	elapsed       int64
	operationName string
	flags         int
	// err/statusErr are atomic for the reason above: an event setter run from
	// another goroutine of the call stack races the sender reading them. err
	// accumulates its ErrorCategory bits with an atomic OR (markSpanError),
	// which needs nothing more than that - the mask carries no other state
	// (DefaultShared.maskErrorCode: getAndUpdate(x -> x | mask)).
	err           atomic.Int32
	statusErr     atomic.Int32
	errorFuncId   int32
	errorString   string
	recovered     atomic.Bool
	asyncId       int32
	asyncSequence int32
	goroutineId   atomic.Int64
	// realTimeTracked records that addRealTimeSampledActiveSpan stored this
	// span in agent.realTimeActiveSpan, so EndSpan deletes only what was
	// stored: the store is gated by atcStreamCount at start, and a viewer that
	// attached or left in between makes the count at end time no guide.
	realTimeTracked atomic.Bool
	eventStack      *stack
	urlStat         *UrlStatEntry
	errorChains     []*exception
	errorChainsLock sync.Mutex
	// refusedChainHeads holds the heads of the new chains the
	// refused throwable in ExceptionContext with the DISABLED state, so a later
	// throwable that continues it reuses that state instead of asking the
	// sampler again; the refused error is not in errorChains, so findError
	// cannot stand in for that. A single slot would lose the older head as soon
	// as a second chain is refused, and the first chain's remaining links would
	// then be charged as a new chain each - exactly what the latch exists to
	// prevent. Ring buffer of maxRefusedChainHeads, oldest evicted: a burst is
	// unbounded, and remembering every refused head would grow without limit.
	// Guarded by errorChainsLock like errorChains.
	refusedChainHeads []error
	refusedChainNext  int
	// errorChainDropLog makes the entry cap log once a span, like
	// eventOverflowLog, so a dropped exception entry is never silent.
	errorChainDropLog atomic.Bool
	// errorChainDrop counts every entry the cap refused, like eventOverflow.
	// errorChainDropLog latches after the first one, so without this the log
	// says a span hit the cap but not by how much - and a retry loop that
	// dropped a handful of links reads exactly like one that dropped
	// thousands. Reported once at the end, where the total is known.
	errorChainDrop atomic.Int32
	finished       atomic.Bool
	// traceRoot is the span whose PSpan carries the error mask, nil when this
	// span is the root itself. An async span is serialized as a PSpanChunk,
	// ChildTrace shares its parent's TraceRoot for the same reason
	// (SpanMessageMapper maps span.traceRoot.shared.errorCode to err), and the
	// the error string and exception chain stay on the recording span.
	traceRoot *span
}

// root returns the span carrying the trace-wide error mask and failure flag.
func (span *span) root() *span {
	if span.traceRoot != nil {
		return span.traceRoot
	}
	return span
}

// firstErrorCategory picks the category a SetFailure call named, defaulting to
// ErrorCategoryUnknown - a failure with no cause attached, which is what
func firstErrorCategory(category []ErrorCategory) ErrorCategory {
	if len(category) > 0 {
		return category[0]
	}
	return ErrorCategoryUnknown
}

// markSpanError ORs one ErrorCategory bit into the root's error mask and
// reports whether the category was marked at all. It is the single point that
// writes span.err: the span level SetError, an event's SetError, a failing
// HTTP status and the SQL.ErrorCount limit all route here, each with its own
// cause, so a transaction that failed for several reasons reports all of them
//
// A category the operator removed with Span.ErrorMark or
// Span.ErrorMarkExclude marks nothing at all, not even
// ConfigurableErrorRecorder.recordError does: the mask is applied only when
// the category is in the enabled set. The false return says exactly that, so
// a caller with more than the mask to write (SetFailure and its URL stat
// flag) can drop the whole verdict.
func (span *span) markSpanError(category ErrorCategory) bool {
	if !span.cfg.marksError(category) {
		return false
	}
	span.root().err.Or(int32(category))
	return true
}

// generateSpanId draws a span id from the whole int64 range, the value space
// span_queue.go) and its Int64 is documented as non-negative, so the full
// range comes from Uint64 reinterpreted as int64. The NULL sentinel is
// redrawn here, not at the call sites, so every id handed out is usable.
//
// It is a var so tests can force a collision; production always draws from rand.
var generateSpanId = func() int64 {
	for {
		if id := int64(rand.Uint64()); id != noneSpanId {
			return id
		}
	}
}

// guarantees it differs from this span's own id and from its parent's, and is
// never the -1 NULL marker; generateSpanId already rules out the sentinel, but
// the guard also covers the generators tests substitute, and a collision with
// either id is a 2^-63 event whose downstream link is wrong when it happens.
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
	span.goroutineId.Store(-1)
	span.asyncId = noneAsyncId
	span.eventStack = newStack()
	span.spanEvents = make([]*spanEvent, 0, span.cfg.spanEventChunkSize)
	span.errorChains = make([]*exception, 0)
	span.overflowSe.parent = &span

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
		endSpanTwiceLog.warnf("abnormal span - EndSpan already called: %s", span.operationName)
		return
	}

	endTime := time.Now()
	span.elapsed = endTime.UnixMilli() - span.startTime.UnixMilli()

	if span.isAsyncSpan() {
		span.endSpanEvent(nil, nil) //async span event
	} else {
		dropSampledActiveSpan(span)
		span.agent.stats.collectResponseTime(span.elapsed)
	}

	// Unbalanced end: leftover events are ended and still recorded. Dropping
	// them would send a span whose event sequence has holes and the
	// collector would rebuild the call tree against the missing parents.
	if leftover := span.eventStack.endAll(); len(leftover) > 0 {
		unclosedEventLog.warnf("abnormal span - %d unclosed event(s) ended by EndSpan: %s", len(leftover), span.operationName)
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

	if dropped := span.errorChainDrop.Load(); dropped > 0 {
		Log("span").Warnf("exception entry limit dropped %d error chain link(s): %s", dropped, span.operationName)
	}

	if span.urlStat != nil {
		// Read from the root: an async worker that failed before this end
		// marked it there. One that ends later is not seen (see newAsyncSpan).
		root := span.root()
		span.agent.enqueueUrlStat(&urlStat{entry: span.urlStat, endTime: endTime, elapsed: span.elapsed, statusErr: int(root.statusErr.Load() | root.err.Load())})
	}

	// Last: the final chunk is enqueued and the url stat read, so nothing this
	// span still owns is written from here on. Seals the Annotation handles
	// taken before the end, which bypass the check in Annotations().
	span.annotations.seal()
}

// warnIfFinished reports whether the span has ended; a setter called after
// EndSpan is dropped, the way spanEvent.warnIfFinished drops one called after
// EndSpanEvent. The final chunk is already on its way to the sender goroutine,
// so nothing written here would be sent (doc/api_contracts.md 3).
func (span *span) warnIfFinished(setter string) bool {
	if !span.finished.Load() {
		return false
	}
	Log("span").Debugf("abnormal span - %s called after EndSpan: %s", setter, span.operationName)
	return true
}

// warnAfterEndSpan reports whether the span has ended; a lifecycle call after
// EndSpan (NewSpanEvent, EndSpanEvent, Inject, NewAsyncSpan) is dropped. The
// final PSpan is already sent, so an event made now could only leak out as a
// non-final PSpanChunk behind it, or advance eventSequence past the range the
// PSpan declared. Throttled: a misinstrumented host hits this once per request.
func (span *span) warnAfterEndSpan(call string) bool {
	if !span.finished.Load() {
		return false
	}
	afterEndSpanLog.warnf("abnormal span - %s called after EndSpan: %s", call, span.operationName)
	return true
}

func (span *span) Inject(writer DistributedTracingContextWriter) {
	if span.warnAfterEndSpan("Inject") {
		return
	}
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
			noEventLog.warnf("abnormal span - has no event: %s", span.operationName)
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

	// This agent has no namespace to send, so the header is omitted rather
	// profiler.cluster.namespace compares the header against its own value:
	// null is accepted for backward compatibility, "" is not, so an empty
	// header makes RequestTraceReader start a new trace instead of continuing.
	// Empty namespaces are normalized to NOT_SET and omitted.

	// Propagate this agent's serviceName when present; v1/v3 emit no such header.
	if span.agent.serviceName != "" {
		writer.Set(HeaderParentServiceName, span.agent.serviceName)
	}

	destinationId := ""
	if se != nil {
		// endPoint (address actually contacted) and destinationId (logical node
		// left unset, never overwrite the one it recorded.
		se.endPoint = cmp.Or(se.endPoint, se.destinationId)
		destinationId = se.destinationId
	} else {
		// Overflowed: the event was dropped, but the destination it recorded
		// was kept for exactly this - the downstream fills acceptorHost,
		// endPoint and remoteAddr from this header (see Extract) and has no
		// other source for them.
		destinationId = span.overflowSe.destination()
	}
	// DefaultRequestTraceWriter does: an empty value carries no less
	// information than a missing header and risks being read as a real host.
	if destinationId != "" {
		writer.Set(HeaderHost, destinationId)
	}

	if IsTraceLogLevelEnabled() {
		Log("span").Tracef("span inject: %v, %d, %d, %s", span.txId, nextSpanId, span.spanId, destinationId)
	}
}

func (span *span) Extract(reader DistributedTracingContextReader) {
	if span.warnAfterEndSpan("Extract") {
		return
	}
	tid, _ := reader.Get(HeaderTraceId)
	txId, continued := continueHeaders(reader)
	if continued {
		span.txId = txId
	} else {
		span.txId = span.agent.generateTransactionId()
		// Only a trace id that was sent and could not be parsed is worth a
		// warning; an entry request carrying no Pinpoint headers is normal, and
		// so is a peer that sent a trace id without the two span id headers.
		if tid != "" {
			if _, _, _, ok := splitTransactionId(tid); !ok {
				malformedTraceIdLog.warnf("malformed trace id header %q: ignoring pinpoint headers, starting a new transaction", tid)
			}
		}
	}

	// Headers that do not describe a hop this span can attach to mean it starts
	// a new transaction, so the remaining Pinpoint headers describe a trace it
	// is not part of: adopting their span/parent ids would record a non-root
	// span pointing at a parent that does not exist in this transaction.
	if !continued {
		span.spanId = generateSpanId()
		span.parentSpanId = -1
		addSampledActiveSpan(span)
		if IsTraceLogLevelEnabled() {
			Log("span").Tracef("span extract: new transaction %s, %d", span.txId, span.spanId)
		}
		return
	}

	spanid, _ := reader.Get(HeaderSpanId)
	if spanid != "" {
		// bitSize 64, not 0: span ids are int64 and 0 means platform int, so
		// a 32-bit build failed to parse an upstream node's id and silently
		// left the span id at zero, breaking the distributed trace.
		if v, err := strconv.ParseInt(spanid, 10, 64); err == nil {
			span.spanId = v
		} else {
			malformedSpanIdLog.warnf("malformed span id header %q: generating a new span id", spanid)
			span.spanId = generateSpanId()
		}
	} else {
		span.spanId = generateSpanId()
	}

	pspanid, _ := reader.Get(HeaderParentSpanId)
	if pspanid != "" {
		if v, err := strconv.ParseInt(pspanid, 10, 64); err == nil {
			span.parentSpanId = v
		} else {
			malformedParentSpanIdLog.warnf("malformed parent span id header %q: treating span as root", pspanid)
			span.parentSpanId = -1
		}
	}

	flag, _ := reader.Get(HeaderFlags)
	if flag != "" {
		span.flags, _ = strconv.Atoi(flag)
	}

	pappname, _ := reader.Get(HeaderParentApplicationName)
	if pappname != "" {
		span.parentAppName = pappname
	}

	// the discarded Atoi result wrote 0, a type neither agent defines.
	papptype, _ := reader.Get(HeaderParentApplicationType)
	if papptype != "" {
		if v, err := strconv.Atoi(papptype); err == nil {
			span.parentAppType = v
		}
	}

	pservicename, _ := reader.Get(HeaderParentServiceName)
	if pservicename != "" {
		span.parentServiceName = pservicename
	}

	host, _ := reader.Get(HeaderHost)
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

// continueHeaders reports whether the inbound headers describe a hop this span
// can attach to, and returns the transaction id to continue when they do.
//
// any one of them missing returns NewTraceHeader. A trace id on its own names a
// transaction but not a position in it, so continuing on it alone records a
// non-root span whose parent is in no trace, and burns a continue-sampler slot
// (isContinueSampled is unconditionally true) for a hop that does not exist.
//
// Presence is the carrier's answer, the second result of Get: a carrier over a
// source that cannot tell a blank header from an absent one reports the blank
// one as absent, and the request starts a new transaction as it did before Get
// reported presence.
//
// The trace id, unlike the span ids, must parse: an unparseable one leaves no
// continue even from a carrier that can report it as present - it names no
// transaction - which is the same divergence, not a second one.
// See doc/java_parity.md.
//
// Both the sampler choice (NewSpanTracerWithReader) and the context extraction
// (Extract) call this, so the two cannot disagree about which trace a request
// belongs to.
func continueHeaders(reader DistributedTracingContextReader) (TransactionId, bool) {
	tid, _ := reader.Get(HeaderTraceId)
	agentId, startTime, sequence, ok := splitTransactionId(tid)
	if !ok {
		return TransactionId{}, false
	}
	if _, ok := reader.Get(HeaderSpanId); !ok {
		return TransactionId{}, false
	}
	if _, ok := reader.Get(HeaderParentSpanId); !ok {
		return TransactionId{}, false
	}
	return TransactionId{agentId, startTime, sequence}, true
}

// splitTransactionId parses an "agentId^startTime^sequence" trace id header
// without allocating (no strings.Split slice) and without risking an
// index-out-of-range panic on a malformed or hostile header. ok is false when
// the header cannot be parsed, and the caller starts a new transaction.
//
//   - The agent id is held to IdValidateUtils' character class and nothing
//     not when it parses this header. The charset half is what protects the
//     rest: the id does not stay inside this process, since Inject writes it
//     back out in the Pinpoint-TraceID of every downstream request and it is
//     reported to the collector as PTransactionId.AgentId, so a header that
//     could carry control bytes or a CRLF would carry them into both.
//   - startTime and sequence go through strconv.ParseInt, which accepts what
//     Long.parseLong accepts: a leading '+' or '-', leading zeros, and any
//     length that still fits an int64. An empty field, a non-digit, or a value
//     that overflows int64 is rejected, as NumberFormatException rejects it.
//     at the next delimiter and never looks past it, so "a^1^2^3" is the
//     transaction "a^1^2" to both agents.
//
// One deliberate gap, unreachable from an agent-emitted header: Long.parseLong
// also accepts non-ASCII Unicode decimal digits (Character.digit), ParseInt
// does not.
func splitTransactionId(tid string) (agentId string, startTime int64, sequence int64, ok bool) {
	i := strings.IndexByte(tid, '^')
	// i < 1 rejects both a missing delimiter and an empty agent id; isIDChars
	// is validateID without the length bound.
	if i < 1 || !isIDChars(tid[:i]) {
		return "", 0, 0, false
	}
	rest := tid[i+1:]
	j := strings.IndexByte(rest, '^')
	if j < 0 {
		return "", 0, 0, false
	}
	startTime, err := strconv.ParseInt(rest[:j], 10, 64)
	if err != nil {
		return "", 0, 0, false
	}
	seq := rest[j+1:]
	if k := strings.IndexByte(seq, '^'); k >= 0 {
		seq = seq[:k]
	}
	sequence, err = strconv.ParseInt(seq, 10, 64)
	if err != nil {
		return "", 0, 0, false
	}
	return tid[:i], startTime, sequence, true
}

func (span *span) NewSpanEvent(operationName string) Tracer {
	if span.warnAfterEndSpan("NewSpanEvent") {
		return span
	}
	// Goroutine-sharing detection is diagnostic only: the event is recorded
	// either way. Returning early here skipped the push, so the caller's paired
	// EndSpanEvent popped the parent's event - and since the check ran only at
	// debug level, the log level decided the shape of the trace. Detection
	// needs the runtime.g offset (goroutine.go); without it there is none.
	if goIdOffset > 0 {
		gid := goIdFromG()
		if !span.goroutineId.CompareAndSwap(-1, gid) && span.goroutineId.Load() != gid {
			sharedGoroutineLog.warnf("span is shared by more than one goroutine: %s", operationName)
		}
	}

	cfg := span.cfg
	// Judged on the position the event has already reserved, not on a load
	// taken before it: the reservation is atomic, so the pair examined here is
	// the pair this event will carry, and no concurrent reservation can move
	// the counters under the decision.
	//
	// se.depth is the depth the new event would be recorded at (eventDepth
	// starts at 1), so se.depth-1 is the number of events already open. That is
	// by isDepthOverflow as maxDepth < index, then incremented and stored as
	// the event's depth. With maxDepth=3 the 4th push (index=3) is still
	// recorded at depth 4, so the deepest recorded level is maxDepth+1.
	// Written as se.depth-1 rather than max+1 because -1 (unlimited) is
	// maxSequence <= sequence.
	se := newSpanEvent(span, operationName)
	if se.sequence >= cfg.spanMaxEventSequence || se.depth-1 > cfg.spanMaxEventDepth {
		span.releaseEventPosition(se.sequence)
		span.eventOverflow.Add(1)
		if span.eventOverflowLog.CompareAndSwap(false, true) {
			Log("span").Warnf("callStack maximum depth/sequence exceeded. (depth=%d, seq=%d)", se.depth, se.sequence)
		}
	} else {
		span.appendSpanEvent(se)
	}
	return span
}

// reserveEventPosition claims the (sequence, depth) pair the next event will be
// recorded at, each counter in one atomic step. eventDepth starts at 1, so a
// single-threaded contract instead, DefaultCallStack.push doing sequence++
// inside push.
//
// It has to be atomic because a span here may be used from several goroutines
// of one call stack (see the note on eventSequence). Reading the two counters
// and incrementing them afterwards handed two concurrent NewSpanEvent calls the
// same sequence and the same depth, and a span carrying a duplicate
// PSpanEvent.sequence breaks the collector's call tree rebuild - not the
// trace-quality problem those atomics are there to keep it at.
//
// The caller owns what it claimed: an event that is pushed gives its depth back
// in spanEvent.end(), one the overflow check refuses gives the position back
// through releaseEventPosition.
func (span *span) reserveEventPosition() (sequence, depth int32) {
	return span.eventSequence.Add(1) - 1, span.eventDepth.Add(1) - 1
}

// releaseEventPosition gives back a position the overflow check refused, the
// keep.
//
// The depth always goes back: no event was pushed, so nothing would ever
// decrement it and the call stack would read as overflowed for the rest of the
// span. The sequence goes back only while it is still the last one handed out,
// which is what keeps eventSequence frozen at its limit for the single call
// stack a Tracer normally instruments; when another goroutine has already
// reserved past it the CAS fails and the number is simply spent. Rolling it
// back unconditionally would hand that number to a second event, and sequence
// uniqueness is worth more than the gap a refused event leaves behind anyway.
func (span *span) releaseEventPosition(sequence int32) {
	span.eventDepth.Add(-1)
	span.eventSequence.CompareAndSwap(sequence+1, sequence)
}

func (span *span) appendSpanEvent(se *spanEvent) {
	span.spanEventLock.Lock()
	defer span.spanEventLock.Unlock()

	// Push only: the counters were advanced by the reserveEventPosition the
	// event was built with.
	span.eventStack.push(se)
}

func (span *span) EndSpanEvent() {
	if span.warnAfterEndSpan("EndSpanEvent") {
		return
	}
	// recover only stops the panic when called by the deferred function
	// itself, so it must stay in this frame and cannot move into
	// endSpanEvent. This guard and the overflow loop in endSpanEvent read
	// eventOverflow at different moments, so a value taken here can still
	// end up on the overflow path - both paths must re-panic it.
	var recovered interface{}
	if span.eventOverflow.Load() == 0 && !span.recovered.Load() {
		recovered = recover()
	}
	span.endSpanEvent(recovered, nil)
}

// EndSpanEventOf ends the innermost span event of tracer, exactly as
// tracer.EndSpanEvent() does, and warns when that event is not se - the
// recorder the caller obtained from tracer.SpanEvent() for the event it meant
// to end. EndSpanEvent takes no target, so a missing or doubled call ends the
// wrong event with nobody's end time and nothing in the log; this is the
// recorder anyway. A function rather than a Tracer method because Tracer is
// implemented outside this module (every plugin test has a mock) and a new
// interface method would break them.
//
// ends whatever is left open and warns through unclosedEventLog. Deferred
// directly, it records a panic on the ended event and re-panics like
// EndSpanEvent. A tracer that is not this agent's span falls back to its own
// EndSpanEvent, which cannot recover a panic from this frame.
func EndSpanEventOf(tracer Tracer, se SpanEventRecorder) {
	span, ok := tracer.(*span)
	if !ok {
		tracer.EndSpanEvent()
		return
	}
	if span.warnAfterEndSpan("EndSpanEvent") {
		return
	}
	// Same guard as EndSpanEvent: recover must be called by the deferred
	// function itself, so the body cannot be shared.
	var recovered interface{}
	if span.eventOverflow.Load() == 0 && !span.recovered.Load() {
		recovered = recover()
	}
	span.endSpanEvent(recovered, se)
}

// endSpanEvent is the unguarded body: EndSpan sets finished first and then
// ends the async span's own event through this path. recovered is the panic
// value EndSpanEvent caught, or nil. want is the event the caller meant to
// end, or nil when the caller did not say (EndSpanEvent).
func (span *span) endSpanEvent(recovered interface{}, want SpanEventRecorder) {
	// agent's SpanData::endDisabledSpanEvent does: a check-then-Add lets two
	// concurrent ends of the same placeholder drive the counter to -1, after
	// which the next real overflow only brings it back to 0 and its end pops
	// a live ancestor off the stack. Overflowed events are never on the
	// stack, so an end that consumed one must not fall through to the pop.
	for pending := span.eventOverflow.Load(); pending > 0; pending = span.eventOverflow.Load() {
		if span.eventOverflow.CompareAndSwap(pending, pending-1) {
			// Cleared once the stack is back within its limits so a later
			// overflow cannot inject the destination of this one.
			if pending == 1 {
				span.overflowSe.destinationId.Store("")
			}
			// The guard in EndSpanEvent read eventOverflow before recover();
			// a placeholder raised in between lands here holding a panic that
			// nothing else will re-raise. Never swallow it.
			if recovered != nil {
				panic(recovered)
			}
			return
		}
	}
	if se, ok := span.eventStack.pop(); ok {
		if want != nil && SpanEventRecorder(se) != want {
			span.warnMisnestedEnd(se, want)
		}
		if v := recovered; v != nil {
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
		se.end()
		span.appendEndedSpanEvent(se)
	} else {
		noEventLog.warnf("abnormal span - has no event: %s", span.operationName)
		if recovered != nil {
			panic(recovered)
		}
	}
}

// warnMisnestedEnd logs that ended is not the event the caller asked for.
// site fires once per request, so the dump rides on the throttle and is
// taken once per dropReportInterval, never for a suppressed call.
func (span *span) warnMisnestedEnd(ended *spanEvent, want SpanEventRecorder) {
	held, ok := misnestedEventLog.acquire()
	if !ok {
		return
	}
	wanted := "<not a span event>"
	if w, ok := want.(*spanEvent); ok {
		wanted = w.operationName
	}
	suppressed := ""
	if held > 0 {
		suppressed = fmt.Sprintf(" (%d similar warning(s) suppressed)", held)
	}
	Log("span").Warnf("abnormal span - EndSpanEventOf ended %s instead of %s: %s%s\n%s",
		ended.operationName, wanted, span.operationName, suppressed, debug.Stack())
}

// appendEndedSpanEvent records a completed event, cutting a chunk for the
// sender once enough have accumulated.
func (span *span) appendEndedSpanEvent(se *spanEvent) {
	span.spanEventLock.Lock()
	defer span.spanEventLock.Unlock()

	// Always appended: EndSpan records the leftover unclosed events through
	// here after setting finished, and they must reach the final chunk. Only
	// the non-final chunk cut is withheld once the span has ended.
	span.spanEvents = append(span.spanEvents, se)
	if !span.finished.Load() && len(span.spanEvents) >= span.cfg.spanEventChunkSize {
		chunk := span.newEventChunk(false)
		if !chunk.enqueue() && IsTraceLogLevelEnabled() {
			Log("span").Tracef("span channel - max capacity reached or closed")
		}
	}
}

func (span *span) newAsyncSpan() Tracer {
	if span.warnAfterEndSpan("NewAsyncSpan") || span.eventOverflow.Load() > 0 {
		return NoopTracer()
	}
	if se, ok := span.eventStack.peek(); ok {
		asyncSpan := defaultSpan(span.agent)

		asyncSpan.cfg = span.cfg // an async span continues under its parent's snapshot
		asyncSpan.txId = span.txId
		asyncSpan.spanId = span.spanId
		// Always the first root, even for an async span forked from an async
		// the root's final chunk is sent at its own EndSpan, so an error recorded
		// ordinary trace has the same limit: DefaultTrace.close()
		// at the root's close, and that is what every normal entry point builds
		// The deferred store lives only on the AsyncDefaultTrace path, whose
		// close() awaits the last child via SpanAsyncStateListener
		// @InterfaceAudience.LimitedPrivate("vert.x"). So this is the same
		asyncSpan.traceRoot = span.traceRoot
		if asyncSpan.traceRoot == nil {
			asyncSpan.traceRoot = span
		}

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
		noEventLog.warnf("abnormal span - has no event: %s", span.operationName)
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
		return &span.overflowSe
	}
	if se, ok := span.eventStack.peek(); ok {
		return se
	}
	noEventLog.warnf("abnormal span - has no event: %s", span.operationName)
	return &defaultNoopSpanEvent
}

func (span *span) IsSampled() bool {
	return true
}

func (span *span) SetError(e error, errorName ...string) {
	// A call stack overflow only blocks span events; the span level error is
	if e == nil || span.warnIfFinished("SetError") {
		return
	}

	errName := errorTypeName(e)
	if len(errorName) > 0 {
		errName = errorName[0]
	}
	id := span.agent.cacheError(errName)
	span.errorFuncId = id
	span.errorString = abbreviateString(e.Error(), maxErrorMessageSize)
	// does not fail the span.
	if !span.cfg.ignoreError(e, errName) {
		span.markSpanError(ErrorCategoryException)
	}
}

// SetFailure marks the transaction failed under the category its caller names,
// ErrorCategoryUnknown when it names none. A category Span.ErrorMark or
// Span.ErrorMarkExclude disabled marks neither the error mask nor the URL
// statistics flag: the two must agree about the same request, or the scatter
// chart would show a failure the URL failed histogram does not have.
func (span *span) SetFailure(category ...ErrorCategory) {
	if span.warnIfFinished("SetFailure") {
		return
	}
	if !span.markSpanError(firstErrorCategory(category)) {
		return
	}
	span.root().statusErr.Store(1)
}

func (span *span) SetServiceType(typ int32) {
	if span.warnIfFinished("SetServiceType") {
		return
	}
	span.serviceType = typ
}

func (span *span) SetRpcName(rpc string) {
	if span.warnIfFinished("SetRpcName") {
		return
	}
	span.rpcName = rpc
}

func (span *span) SetRemoteAddress(remoteAddress string) {
	if span.warnIfFinished("SetRemoteAddress") {
		return
	}
	span.remoteAddr = remoteAddress
}

func (span *span) SetEndPoint(endPoint string) {
	if span.warnIfFinished("SetEndPoint") {
		return
	}
	span.endPoint = endPoint
	// requestAdaptor.getAcceptorHost() - the address the request arrived on,
	// which is this endPoint - when the caller sent no Pinpoint-Host header.
	// Extract cannot do that itself: the server plugins set the endPoint only
	// after it ran, so the fallback is applied here and an explicit header or
	// SetAcceptorHost still wins.
	if span.acceptorHost == "" {
		span.acceptorHost = endPoint
	}
}

func (span *span) SetAcceptorHost(host string) {
	if span.warnIfFinished("SetAcceptorHost") {
		return
	}
	span.acceptorHost = host
}

func (span *span) Annotations() Annotation {
	if span.warnIfFinished("Annotations") {
		return &noopAnnotation{}
	}
	return &span.annotations
}

func (span *span) SetLogging(logInfo int32) {
	if span.warnIfFinished("SetLogging") {
		return
	}
	span.loggingInfo = logInfo
}

func (span *span) collectUrlStat(stat *UrlStatEntry, force bool) {
	if span.cfg.collectUrlStat {
		span.urlStat = mergeUrlStat(span.urlStat, stat, force)
	}
}

// mergeUrlStat applies a recorded entry to the one a span already holds and
// returns the entry to keep. Shared by span and noopSpan so both paths follow
//
// null -> value CAS while setHttpMethod and HttpStatusCodeRecorder are plain
// setters the last caller owns. A framework that recorded the matched route
// first must not have it replaced by a later, less precise layer; the status
// code, though, is legitimately final only once the response exists, so
// making the whole entry first-wins would freeze it at the first caller's
// setUriTemplate(value, true).
//
// The caller's entry is copied, never stored or written to, so later caller
// mutation cannot change the span's statistic.
func mergeUrlStat(current, stat *UrlStatEntry, force bool) *UrlStatEntry {
	entry := *stat
	if entry.Url == "" {
		entry.Url = urlStatUnknown
	}
	if current != nil && !force && current.Url != urlStatUnknown {
		entry.Url = current.Url
	}
	return &entry
}

func (span *span) AddMetric(metric string, value interface{}) {
	// collectUrlStat is reached only from here, and EndSpan reads span.urlStat
	// after enqueueing the final chunk: a late write both races that read and
	// sets a stat nothing enqueues.
	if span.warnIfFinished("AddMetric") {
		return
	}

	if metric == MetricURLStat || metric == MetricURLStatForce {
		if entry, ok := value.(*UrlStatEntry); ok && entry != nil {
			span.collectUrlStat(entry, metric == MetricURLStatForce)
		} else {
			Log("span").Warnf("AddMetric: value for %s must be *UrlStatEntry", metric)
		}
	}
}

func (span *span) JsonString() []byte {
	m := make(map[string]interface{}, 0)
	m["RpcName"] = span.rpcName
	m["EndPoint"] = span.endPoint
	m["RemoteAddr"] = span.remoteAddr
	m["Err"] = span.err.Load()
	m["Annotations"] = span.annotations.getList()
	b, _ := json.Marshal(m)
	return b
}

// canAddErrorChain reports whether another exception entry fits. Callers hold
// errorChainsLock: it reads errorChains, which traceCallStack appends to under
// that lock.
func (span *span) canAddErrorChain() bool {
	if span.errorChains == nil {
		return false
	}
	if len(span.errorChains) < max(minErrorChainEntry, span.cfg.errorMaxChainDepth) {
		return true
	}
	span.errorChainDrop.Add(1)
	if span.errorChainDropLog.CompareAndSwap(false, true) {
		Log("span").Warnf("exception entry limit reached, dropping further error chain links (entries=%d)", len(span.errorChains))
	}
	return false
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

	// slices.SortStableFunc, not sort.Slice: this runs on the request goroutine
	// per chunk, and sort.Slice builds a reflect-based swapper for the slice on
	// every call. Stable, so that should two events ever share a sequence after
	// all, the order this hands to the depth compression and the startElapsed
	// deltas below - both of which read the event next to them - is the order
	// they were recorded in, not one that varies run to run.
	slices.SortStableFunc(chunk.eventChunk, func(a, b *spanEvent) int {
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
			// Seed the compression baseline with the first event's own depth,
			// event still carries its real depth; compression starts at i == 1.
			prevDepth = se.depth
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
