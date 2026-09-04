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

func defaultSpanEvent(span *span, operationName string) *spanEvent {
	se := spanEvent{}

	se.parentSpan = span
	se.startTime = time.Now().UnixMilli()
	se.startElapsed = 0
	se.sequence = span.eventSequence.Load()
	se.depth = span.eventDepth.Load()
	se.operationName = operationName
	se.endPoint = ""
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
	se := defaultSpanEvent(span, operationName)
	se.apiId = span.agent.cacheSpanApi(operationName, apiTypeDefault)

	return se
}

func newSpanEventGoroutine(span *span) *spanEvent {
	se := defaultSpanEvent(span, "")

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
	se.parentSpan.eventDepth.Add(-1)
	if !se.isTimeFixed {
		se.endElapsed = time.Now().UnixMilli() - se.startTime
	}
	if IsTraceLogLevelEnabled() {
		Log("span").Tracef("endSpanEvent: %s", se.operationName)
	}
	se.finished.Store(true)
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
	// An error matching Error.IgnoreErrors (IgnoreErrorHandler) keeps its
	// exception info but skips that failure marking.
	if !cfg.ignoreError(e, errName) {
		se.parentSpan.err = 1
	}
	if cfg.errorTraceCallStack && se.parentSpan.canAddErrorChain() {
		se.exceptionId = se.parentSpan.traceCallStack(e, errName, cfg.errorCallStackDepth, time.UnixMilli(se.startTime))
		se.Annotations().AppendLong(AnnotationExceptionChainId, se.exceptionId)
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

	var nsql, param string
	if cfg.sqlEnableRawSqlCache {
		nsql, param = agent.normalizeSql(sql)
	} else {
		nsql, param = newSqlNormalizer(sql).run()
	}
	// nsql is already bounded by the normalizer; cacheSql/cacheSqlUid bound the
	// cache key again for any other caller. param is never abbreviated: the
	// server splits it on ',' to fill the <idx>#/<idx>$ placeholders of nsql,
	// so a cut param leaves placeholders exposed. Its size is already capped by
	// maxSqlSize because the normalizer stops emitting parameters once nsql is
	// full (see sqlNormalizerBuilder). MaxBindValueSize applies to bind values
	// only, as in the Java agent; a limit of 0 means bind value tracing is off,
	// not that every value should become an "...(0)" marker.
	if cfg.sqlMaxBindValueSize > 0 {
		args = abbreviateString(args, cfg.sqlMaxBindValueSize)
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
