package ppmongo

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/event"
)

type spanKey struct {
	ConnectionID string
	RequestID    int64
}

type monitor struct {
	sync.Mutex
	spans map[spanKey]pinpoint.Tracer
}

func (m *monitor) Started(ctx context.Context, evt *event.CommandStartedEvent) {
	tracer := pinpoint.FromContext(ctx)
	if tracer == nil {
		return
	}

	//fmt.Println("db= " + evt.DatabaseName)
	//fmt.Println("connId= " + evt.ConnectionID)
	//fmt.Println("reqId= " + strconv.FormatInt(evt.RequestID, 10))
	//fmt.Println("command= " + evt.CommandName)

	hostname := getHost(evt.ConnectionID)
	b, _ := bson.MarshalExtJSON(evt.Command, false, false)

	//fmt.Println("hostname= " + hostname)
	//fmt.Println("json= " + string(b))

	dbInfo := &pinpoint.DBInfo{}
	dbInfo.DBType = pinpoint.ServiceTypeMongo
	dbInfo.QueryType = pinpoint.ServiceTypeMongoExecuteQuery
	dbInfo.DBName = evt.DatabaseName
	dbInfo.DBHost = hostname

	tracer = pinpoint.NewDatabaseTracer(ctx, "mongodb."+evt.CommandName, dbInfo)
	tracer.SpanEvent().Annotations().AppendString(pinpoint.AnnotationMongoCollectionInfo, collName(evt))
	tracer.SpanEvent().Annotations().AppendStringString(pinpoint.AnnotationMongoJasonData, string(b), "")

	key := spanKey{
		ConnectionID: evt.ConnectionID,
		RequestID:    evt.RequestID,
	}

	m.Lock()
	defer m.Unlock()
	m.spans[key] = tracer
}

func collName(e *event.CommandStartedEvent) string {
	coll := e.Command.Lookup(e.CommandName)
	collName, _ := coll.StringValueOK()
	return collName
}

func queryString(e *event.CommandStartedEvent) string {
	qry := e.Command.Lookup("documents")
	queryStr, _ := qry.StringValueOK()
	return queryStr
}

func (m *monitor) Succeeded(ctx context.Context, evt *event.CommandSucceededEvent) {
	m.Finished(&evt.CommandFinishedEvent, nil)
}

func (m *monitor) Failed(ctx context.Context, evt *event.CommandFailedEvent) {
	m.Finished(&evt.CommandFinishedEvent, fmt.Errorf("%s", evt.Failure))
}

func (m *monitor) Finished(evt *event.CommandFinishedEvent, err error) {
	key := spanKey{
		ConnectionID: evt.ConnectionID,
		RequestID:    evt.RequestID,
	}

	m.Lock()
	defer m.Unlock()
	tracer, ok := m.spans[key]

	if ok {
		delete(m.spans, key)
	} else {
		return
	}

	tracer.SpanEvent().SetError(err)
	tracer.EndSpanEvent()
}

func NewMonitor() *event.CommandMonitor {
	m := &monitor{
		spans: make(map[spanKey]pinpoint.Tracer),
	}

	return &event.CommandMonitor{
		Started:   m.Started,
		Succeeded: m.Succeeded,
		Failed:    m.Failed,
	}
}

func getHost(connId string) string {
	hostname := connId
	if idx := strings.IndexByte(connId, '['); idx >= 0 {
		hostname = hostname[:idx]
	}
	if idx := strings.IndexByte(hostname, ':'); idx >= 0 {
		hostname = hostname[:idx]
	}
	return hostname
}
