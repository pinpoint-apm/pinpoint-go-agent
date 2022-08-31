package pinpoint

import (
	"time"
)

type spanEvent struct {
	parentSpan    *span
	serviceType   int32
	sequence      int32
	depth         int32
	startTime     time.Time
	startElapsed  time.Duration
	endElapsed    time.Duration
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
}

var asyncApiId = int32(0)

func defaultSpanEvent(span *span, operationName string) *spanEvent {
	se := spanEvent{}

	se.parentSpan = span
	se.startTime = time.Now()
	se.startElapsed = se.startTime.Sub(span.startTime)
	se.sequence = span.eventSequence
	se.depth = span.eventDepth
	se.operationName = operationName
	se.endPoint = ""
	se.asyncId = NoneAsyncId
	se.asyncSeqGen = 0
	se.serviceType = ServiceTypeGoFunction
	se.isTimeFixed = false

	log("span").Debug("newSpanEvent: ", se.operationName, se.sequence, se.depth, se.startTime)

	return &se
}

func newSpanEvent(span *span, operationName string) *spanEvent {
	se := defaultSpanEvent(span, operationName)
	se.apiId = span.agent.cacheSpanApiId(operationName, ApiTypeDefault)

	return se
}

func newSpanEventGoroutine(span *span) *spanEvent {
	se := defaultSpanEvent(span, "")

	//Asynchronous Invocation
	if asyncApiId == 0 {
		asyncApiId = span.agent.cacheSpanApiId("Goroutine Invocation", ApiTypeInvocation)
	}
	se.apiId = asyncApiId
	se.serviceType = ServiceTypeAsync

	return se
}

func (se *spanEvent) end() {
	se.parentSpan.eventDepth--
	if !se.isTimeFixed {
		se.endElapsed = time.Now().Sub(se.startTime)
	}

	log("span").Debug("endSpanEvent: ", se.operationName)
}

func (se *spanEvent) generateNextSpanId() int64 {
	se.nextSpanId = generateSpanId()
	return se.nextSpanId
}

func (se *spanEvent) SetError(e error) {
	if e == nil {
		return
	}

	id := se.parentSpan.agent.cacheErrorFunc(se.operationName)
	se.errorFuncId = id
	se.errorString = e.Error()
}

func (se *spanEvent) SetApiId(id int32) {
	se.apiId = id
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

	normalizer := newSqlNormalizer(sql)
	nsql, param := normalizer.run()
	id := se.parentSpan.agent.cacheSql(nsql)
	se.annotations.AppendIntStringString(AnnotationSqlId, id, param, args)
}

func (se *spanEvent) Annotations() Annotation {
	return &se.annotations
}

func (se *spanEvent) FixDuration(start time.Time, end time.Time) {
	se.startTime = start
	se.startElapsed = start.Sub(se.parentSpan.startTime)
	se.endElapsed = end.Sub(start)
	se.isTimeFixed = true
}
