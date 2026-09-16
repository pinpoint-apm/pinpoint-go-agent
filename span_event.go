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
// read off the span here, or two concurrent events could share a sequence.
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

// warnIfFinished reports whether the event has ended, logging the name of the
// setter that arrived too late.
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
	// Marking the span failed drives PSpan.err, the URL stat failed histogram
	// and the scatter failure point. An error matching Span.IgnoreErrors keeps
	// its exception info but skips that marking.
	if !cfg.ignoreError(e, errName) {
		se.parentSpan.markSpanError(ErrorCategoryException)
	}
	// The entry cap is checked inside traceCallStack under errorChainsLock;
	// reading it here would race a concurrent SetError on another goroutine of
	// the same call stack.
	if cfg.errorTraceCallStack {
		// A chain the Error.NewThroughput limiter denied, or one refused by the
		// entry cap, never reaches the wire, so it gets no annotation either.
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
	// An empty statement records nothing: no annotation, no SQL count, no
	// metadata. The sql/driver wrapper routes Begin, BeginTx, Commit and
	// Rollback through setSqlSpanEvent with sql == "" (newSqlSpanEventNoSql),
	// so this guard is what keeps a transaction boundary from carrying an
	// empty SQL annotation.
	if sql == "" || se.warnIfFinished("SetSQL") {
		return
	}

	agent := se.agent()
	cfg := se.config()

	// A statement past the normalization cap is dropped whole - not counted,
	// not normalized, not annotated. Cutting it and normalizing the rest would
	// lose a placeholder when the cut lands inside a literal; see
	// maxSqlNormalizeLength.
	if !sqlNormalizable(sql) {
		if IsDebugLogLevelEnabled() {
			Log("span").Debugf("SetSQL: statement of %d bytes past the normalization cap dropped", len(sql))
		}
		return
	}

	var nsql, param string
	if cfg.sqlEnableRawSqlCache {
		nsql, param = agent.normalizeSql(sql)
	} else {
		nsql, param = newSqlNormalizer(sql, cfg.sqlRemoveComments).run()
	}
	// Neither nsql nor param is abbreviated: cacheSql and cacheSqlUid abbreviate
	// only the text they publish, the id and the UID cover the whole normalized
	// statement, and the server splits param on ',' to fill the <idx>#/<idx>$
	// placeholders of nsql, so a cut param leaves placeholders exposed.
	//
	// args is bounded by maxBindValueAnnotationSize rather than by
	// SQL.MaxBindValueSize itself, so a list the driver writers composed within
	// the limit passes through untouched while args from a caller that bounds
	// nothing still cannot grow the span without limit. See
	// doc/api_contracts.md 7.
	if cfg.sqlMaxBindValueSize > 0 {
		args = abbreviateString(args, maxBindValueAnnotationSize(cfg.sqlMaxBindValueSize))
	}

	if cfg.sqlTraceQueryStat {
		id := agent.cacheSqlUid(nsql)
		if id == nil {
			return
		}
		se.annotations.appendOwnedBytesStringString(AnnotationSqlUid, id, param, args)
	} else {
		id := agent.cacheSql(nsql)
		if id == 0 {
			return
		}
		se.annotations.AppendIntStringString(AnnotationSqlId, id, param, args)
	}

	// A trace that runs SQL.ErrorCount queries is marked failed: an N+1 loop is
	// what the count exists to surface. Count and flag live on the trace root,
	// so queries spread over async spans add up. An error already set is never
	// re-marked, and a finished span is skipped for the same reason SetError
	// skips one (doc/api_contracts.md 5). The counting sits after the
	// annotation, so a statement whose metadata registration failed returned
	// above and is not counted: a span is never marked for queries the UI
	// cannot show. The cause is ErrorCategorySql, so an operator who does not
	// want an N+1 pattern to fail the transaction can exclude that one category
	// (Span.ErrorMarkExclude) and keep the rest.
	root := se.parentSpan.root()
	if cfg.sqlErrorCount > 0 && root.err.Load() == 0 && !se.parentSpan.finished.Load() {
		if int(root.sqlCount.Add(1)) >= cfg.sqlErrorCount {
			se.parentSpan.markSpanError(ErrorCategorySql)
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
