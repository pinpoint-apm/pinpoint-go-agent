// Package ppmongo instruments the mongodb/mongo-go-driver package (https://github.com/mongodb/mongo-go-driver).
//
// This package instruments the mongo-go-driver calls.
// Use the NewMonitor as Monitor field of mongo-go-driver's ClientOptions.
//
//	opts := options.Client()
//	opts.Monitor = ppmongo.NewMonitor()
//	client, err := mongo.Connect(ctx, opts)
//
// It is necessary to pass the context containing the pinpoint.Tracer to mongo.Client.
//
//	collection := client.Database("testdb").Collection("example")
//	ctx := pinpoint.NewContext(context.Background(), tracer)
//	collection.InsertOne(ctx, bson.M{"foo": "bar", "apm": "pinpoint"})
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

const maxJsonSize = 64 * 1024

type monitor struct {
	sync.Mutex
	spans map[spanKey]pinpoint.Tracer
}

func (m *monitor) Started(ctx context.Context, evt *event.CommandStartedEvent) {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return
	}

	hostname := getHost(evt.ConnectionID)
	dbInfo := &pinpoint.DBInfo{
		DBType:    pinpoint.ServiceTypeMongo,
		QueryType: pinpoint.ServiceTypeMongoExecuteQuery,
		DBName:    evt.DatabaseName,
		DBHost:    hostname,
	}
	tracer = pinpoint.NewDatabaseTracer(ctx, "mongodb."+evt.CommandName, dbInfo)

	a := tracer.SpanEvent().Annotations()
	a.AppendString(pinpoint.AnnotationMongoCollectionInfo, collectionName(evt))
	b, _ := bson.MarshalExtJSON(evt.Command, false, false)
	if b != nil {
		a.AppendStringString(pinpoint.AnnotationMongoJasonData, abbreviateJson(b, maxJsonSize), "")
	}

	key := spanKey{
		ConnectionID: evt.ConnectionID,
		RequestID:    evt.RequestID,
	}

	m.Lock()
	m.spans[key] = tracer
	m.Unlock()
}

func collectionName(e *event.CommandStartedEvent) string {
	coll := e.Command.Lookup(e.CommandName)
	collName, _ := coll.StringValueOK()
	return collName
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
	tracer, ok := m.spans[key]
	if !ok {
		m.Unlock()
		return
	}

	defer tracer.EndSpanEvent()
	delete(m.spans, key)
	m.Unlock()
	tracer.SpanEvent().SetError(err)
}

// NewMonitor returns a *event.CommandMonitor ready to instrument.
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

func abbreviateJson(b []byte, length int) string {
	if len(b) <= length {
		return string(b)
	}
	return string(b[:length]) + "...(" + fmt.Sprint(length) + ")"
}
