package pinpoint

import (
	"sync/atomic"
	"time"
)

type spanEvent struct {
	parentSpan    *span
	serviceType   int32
	sequence      int32
	depth         int32
	startTime     int64
	startElapsed  int64
	endElapsed    int64
	operationName string
	nextSpanId    int64
	annotations   annotation
	endPoint      string
	destinationId string
	errorFuncId   int32
	errorString   string
	asyncId       int32
	asyncSeqGen   int32
	apiId         int32
	isTimeFixed   bool
	exceptionId   int64
	// finished is set by end(). From then on the event may sit in a chunk the
	// sender goroutine is serializing, so every public setter becomes a no-op
	// instead of racing with makePSpanEvent.
	finished atomic.Bool
}

// defaultSpanEvent builds an event at the position sequence/depth, which the
// caller must have claimed with span.reserveEventPosition. The pair is never
// read off the span here: loading the counters and letting the push increment
// them afterwards is what let two concurrent events share a sequence.
func defaultSpanEvent(span *span, operationName string, sequence int32, depth int32) *spanEvent {
	se := spanEvent{}

	se.parentSpan = span
	se.startTime = time.Now().UnixMilli()
	se.startElapsed = 0
	se.sequence = sequence
	se.depth = depth
	se.operationName = operationName
	se.endPoint = ""
	se.nextSpanId = noneSpanId
	se.asyncId = noneAsyncId
	se.asyncSeqGen = 0
	se.serviceType = ServiceTypeGoFunction
	se.isTimeFixed = false

	if IsTraceLogLevelEnabled() {
		Log("span").Tracef("newSpanEvent: %s, %d, %d, %s", se.operationName, se.sequence, se.depth, time.Now())
	}

	return &se
}

func newSpanEvent(span *span, operationName string) *spanEvent {
	sequence, depth := span.reserveEventPosition()
	se := defaultSpanEvent(span, operationName, sequence, depth)
	se.apiId = span.agent.cacheSpanApi(operationName, apiTypeDefault)

	return se
}

func newSpanEventGoroutine(span *span) *spanEvent {
	// Reserved like any other event, and never refused: newAsyncSpan builds
	// the span it passes here, so the reservation is the span's first, (0, 1),
	// which no call stack limit can be below.
	sequence, depth := span.reserveEventPosition()
	se := defaultSpanEvent(span, "", sequence, depth)

	//Asynchronous Invocation
	apiId := atomic.LoadInt32(&span.agent.asyncApiId)
	if apiId == 0 {
		apiId = span.agent.cacheSpanApi("Goroutine Invocation", apiTypeInvocation)
		atomic.StoreInt32(&span.agent.asyncApiId, apiId)
	}
	se.apiId = apiId
	se.serviceType = ServiceTypeAsync

	return se
}

func (se *spanEvent) end() {
	// Idempotent: a second end would decrement eventDepth below the stack.
	if se.finished.Swap(true) {
		return
	}
	se.parentSpan.eventDepth.Add(-1)
	if !se.isTimeFixed {
		se.endElapsed = time.Now().UnixMilli() - se.startTime
	}
	if IsTraceLogLevelEnabled() {
		Log("span").Tracef("endSpanEvent: %s", se.operationName)
	}
	// After finished: an Annotation handle taken before the end bypasses the
	// check in Annotations(), so the collector is sealed too.
	se.annotations.seal()
}

// warnIfFinished reports whether the event has ended; a setter called after
// EndSpanEvent is dropped, mirroring the C++ agent's warnIfFinished.
func (se *spanEvent) warnIfFinished(setter string) bool {
	if !se.finished.Load() {
		return false
	}
	Log("span").Debugf("abnormal span event - %s called after EndSpanEvent: %s", setter, se.operationName)
	return true
}

func (se *spanEvent) generateNextSpanId() int64 {
	se.nextSpanId = nextSpanId(se.parentSpan.spanId, se.parentSpan.parentSpanId)
	return se.nextSpanId
}

func (se *spanEvent) SetError(e error, errorName ...string) {
	// After EndSpan the span is on its way to the sender goroutine; a
	// retained recorder must not write into it (see doc/api_contracts.md 5).
	if e == nil || se.warnIfFinished("SetError") || se.parentSpan.finished.Load() {
		return
	}

	var errName string
	if len(errorName) > 0 {
		errName = errorName[0]
	} else {
		errName = errorTypeName(e)
	}

	id := se.agent().cacheError(errName)
	se.errorFuncId = id
	se.errorString = abbreviateString(e.Error(), maxErrorMessageSize)

	cfg := se.config()
	// As in the Java agent, an error on any event fails the transaction:
	// PSpan.err, the URL stat failed histogram and the scatter failure point.
	// The cause is ErrorCategoryException wherever the error was recorded, as
	// Java's AbstractRecorder.recordException reports EXCEPTION from every
	// recorder. An error matching Span.IgnoreErrors (IgnoreErrorHandler)
	// keeps its exception info but skips that failure marking.
	if !cfg.ignoreError(e, errName) {
		se.parentSpan.markSpanError(ErrorCategoryException)
	}
	if cfg.errorTraceCallStack && se.parentSpan.canAddErrorChain() {
		// A chain the Error.NewThroughput limiter denied is not on the wire, so
		// it gets no annotation either - Java's DISABLED sampling state skips
		// the EXCEPTION_CHAIN_ID annotation the same way.
		if eid := se.parentSpan.traceCallStack(e, errName, cfg.errorCallStackDepth, time.UnixMilli(se.startTime)); eid != noExceptionChainId {
			se.exceptionId = eid
			se.Annotations().AppendLong(AnnotationExceptionChainId, eid)
		}
	}
}

func (se *spanEvent) SetServiceType(typ int32) {
	if se.warnIfFinished("SetServiceType") {
		return
	}
	se.serviceType = typ
}

func (se *spanEvent) SetDestination(id string) {
	if se.warnIfFinished("SetDestination") {
		return
	}
	se.destinationId = id
}

func (se *spanEvent) SetEndPoint(endPoint string) {
	if se.warnIfFinished("SetEndPoint") {
		return
	}
	se.endPoint = endPoint
}

func (se *spanEvent) SetSQL(sql string, args string) {
	if sql == "" || se.warnIfFinished("SetSQL") {
		return
	}

	agent := se.agent()
	cfg := se.config()

	// As in the Java agent's DefaultSqlCountService, a span that executes
	// SQL.ErrorCount queries is marked failed - an N+1 loop is a trace the
	// server should show as an error. Java skips a transaction whose error code
	// is already set, so the count never re-marks a recorded error; a finished
	// span is skipped for the same reason SetError does (doc/api_contracts.md 5).
	// Count and flag on the trace root, so queries spread over async spans add
	// up, which is what Java does: recordSqlCount is handed the trace root's
	// Shared (WrappedSpanEventRecorder.java:112) and the counter lives there
	// (DefaultSqlCountService.java:16,21). The C++ agent deliberately differs
	// here, counting per span so an async child has its own sql_count_
	// (src/span.h:644-646); it is not the reference for this placement.
	// The cause is ErrorCategorySql, so an operator who does not want an N+1
	// pattern to fail the transaction can drop just that one with
	// Span.ErrorMarkExclude and keep the counting - Java applies the same filter
	// inside the recorder, downstream of DefaultSqlCountService.
	root := se.parentSpan.root()
	if cfg.sqlErrorCount > 0 && root.err.Load() == 0 && !se.parentSpan.finished.Load() {
		if int(root.sqlCount.Add(1)) >= cfg.sqlErrorCount {
			se.parentSpan.markSpanError(ErrorCategorySql)
		}
	}

	var nsql, param string
	if cfg.sqlEnableRawSqlCache {
		nsql, param = agent.normalizeSql(sql)
	} else {
		nsql, param = newSqlNormalizer(sql, cfg.sqlRemoveComments).run()
	}
	// nsql is the whole normalized SQL, as in the Java agent: cacheSql and
	// cacheSqlUid abbreviate the text they publish, and the UID hashes the
	// untruncated SQL. param is never abbreviated either - the server splits it
	// on ',' to fill the <idx>#/<idx>$ placeholders of nsql, so a cut param
	// leaves placeholders exposed. MaxBindValueSize applies to bind values
	// only, as in the Java agent; a limit of 0 means bind value tracing is off,
	// not that every value should become an "...(0)" marker.
	//
	// The allowance is what the bind value writers append past the limit - the
	// Java agent puts its "...(count)" marker there too. Without it this would
	// cut their marker back off and replace it with its own, reporting the
	// bytes dropped instead of the bind values. SetSQL is public, so the bound
	// stays for a caller that composes args itself and bounds nothing.
	if cfg.sqlMaxBindValueSize > 0 {
		args = abbreviateString(args, cfg.sqlMaxBindValueSize+maxBindValueMarkerSize)
	}

	if cfg.sqlTraceQueryStat {
		if id := agent.cacheSqlUid(nsql); id != nil {
			se.annotations.AppendBytesStringString(AnnotationSqlUid, id, param, args)
		}
	} else {
		if id := agent.cacheSql(nsql); id != 0 {
			se.annotations.AppendIntStringString(AnnotationSqlId, id, param, args)
		}
	}
}

func (se *spanEvent) Annotations() Annotation {
	if se.warnIfFinished("Annotations") {
		return &noopAnnotation{}
	}
	return &se.annotations
}

func (se *spanEvent) FixDuration(start time.Time, end time.Time) {
	if se.warnIfFinished("FixDuration") {
		return
	}
	se.startTime = start.UnixMilli()
	se.endElapsed = end.UnixMilli() - se.startTime
	se.isTimeFixed = true
}

func (se *spanEvent) agent() *agent {
	return se.parentSpan.agent
}

func (se *spanEvent) config() *configSnapshot {
	return se.parentSpan.cfg
}
