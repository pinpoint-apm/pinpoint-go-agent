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
}

var exceptionIdGen int64 = 0

func defaultSpanEvent(span *span, operationName string) *spanEvent {
	se := spanEvent{}

	se.parentSpan = span
	se.startTime = time.Now().UnixMilli()
	se.startElapsed = 0
	se.sequence = span.eventSequence
	se.depth = span.eventDepth
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
	se.parentSpan.eventDepth--
	if !se.isTimeFixed {
		se.endElapsed = time.Now().UnixMilli() - se.startTime
	}
	if IsTraceLogLevelEnabled() {
		Log("span").Tracef("endSpanEvent: %s", se.operationName)
	}
}

func (se *spanEvent) generateNextSpanId() int64 {
	se.nextSpanId = generateSpanId()
	return se.nextSpanId
}

func (se *spanEvent) SetError(e error, errorName ...string) {
	if e == nil {
		return
	}

	var errName string
	if len(errorName) > 0 {
		errName = errorName[0]
	} else {
		errName = "error"
	}

	id := se.agent().cacheError(errName)
	se.errorFuncId = id
	se.errorString = e.Error()

	cfg := se.config()
	if cfg.errorTraceCallStack && se.parentSpan.canAddErrorChain() {
		se.exceptionId = se.parentSpan.traceCallStack(e, cfg.errorCallStackDepth)
		se.Annotations().AppendLong(AnnotationExceptionChainId, se.exceptionId)
	}
}

func (se *spanEvent) SetServiceType(typ int32) {
	se.serviceType = typ
}

func (se *spanEvent) SetDestination(id string) {
	se.destinationId = id
}

func (se *spanEvent) SetEndPoint(endPoint string) {
	se.endPoint = endPoint
}

func (se *spanEvent) SetSQL(sql string, args string) {
	if sql == "" {
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
	return &se.annotations
}

func (se *spanEvent) FixDuration(start time.Time, end time.Time) {
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
