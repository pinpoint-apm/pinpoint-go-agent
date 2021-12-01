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
	duration      time.Duration
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

func newSpanEvent(span *span, operationName string) *spanEvent {
	se := spanEvent{}

	se.parentSpan = span
	se.startTime = time.Now()
	se.startElapsed = se.startTime.Sub(span.startTime)
	se.sequence = span.eventSequence
	se.depth = span.eventDepth
	se.operationName = operationName
	se.endPoint = ""
	se.asyncId = 0
	se.asyncSeqGen = 0
	se.serviceType = ServiceTypeGoFunction
	se.isTimeFixed = false

	return &se
}

func (se *spanEvent) end() {
	se.parentSpan.eventDepth--
	if !se.isTimeFixed {
		se.duration = time.Now().Sub(se.startTime)
	}
}

func (se *spanEvent) generateNextSpanId() int64 {
	se.nextSpanId = generateSpanId()
	return se.nextSpanId
}

func (se *spanEvent) SetError(e error) {
	if e == nil {
		return
	}

	id := se.parentSpan.agent.CacheErrorFunc(se.operationName)
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

func (se *spanEvent) SetSQL(sql string) {
	if sql == "" {
		return
	}

	normalizer := newSqlNormalizer(sql)
	nsql, param := normalizer.run()
	id := se.parentSpan.agent.CacheSql(nsql)
	se.annotations.AppendIntStringString(20, id, param, "" /* bind value for prepared stmt */)
}

func (span *spanEvent) Annotations() Annotation {
	return &span.annotations
}

func (se *spanEvent) FixDuration(start time.Time, end time.Time) {
	se.startTime = start
	se.startElapsed = start.Sub(se.parentSpan.startTime)
	se.duration = end.Sub(start)
	se.isTimeFixed = true
}
