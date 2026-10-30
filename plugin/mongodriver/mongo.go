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
	"errors"
	"fmt"

	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongomonitor/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/event"
)

// The Mongo.* options are shared with the mongodriver plugin and live in
// ppmongomonitor; these names keep them on this package.
const (
	CfgMongoRecordCommand  = ppmongomonitor.CfgMongoRecordCommand
	CfgMongoCommandMaxSize = ppmongomonitor.CfgMongoCommandMaxSize
)

var (
	// WithMongoRecordCommand sets whether the command document is recorded on
	// the span event (Mongo.RecordCommand, default true).
	WithMongoRecordCommand = ppmongomonitor.WithMongoRecordCommand
	// WithMongoCommandMaxSize sets the size in bytes the recorded command
	// document is cut to; a command larger than this is not converted at all
	// (Mongo.CommandMaxSize, default 65536). A value of 0 or less keeps the default.
	WithMongoCommandMaxSize = ppmongomonitor.WithMongoCommandMaxSize
)

// monitor is the shared span map behind the driver's three callbacks.
type monitor struct {
	*ppmongomonitor.Monitor
}

func (m *monitor) Started(ctx context.Context, evt *event.CommandStartedEvent) {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return
	}

	dbInfo := &pinpoint.DBInfo{
		DBType:    pinpoint.ServiceTypeMongo,
		QueryType: pinpoint.ServiceTypeMongoExecuteQuery,
		DBName:    evt.DatabaseName,
		DBHost:    ppmongomonitor.Host(evt.ConnectionID),
	}
	tracer = pinpoint.NewDatabaseTracer(ctx, ppmongomonitor.OperationName(evt.CommandName), dbInfo)

	collection := collectionName(evt)
	a := tracer.SpanEvent().Annotations()
	a.AppendString(pinpoint.AnnotationMongoCollectionInfo, collection)
	if record, maxSize := ppmongomonitor.CommandSettings(pinpoint.GetConfig()); record {
		if command := commandAnnotation(evt, collection, maxSize); command != "" {
			a.AppendStringString(pinpoint.AnnotationMongoJasonData, command, "")
		}
	}

	m.Monitor.Started(ppmongomonitor.Key{ConnectionID: evt.ConnectionID, RequestID: evt.RequestID}, tracer)
}

// commandAnnotation renders the command as extended JSON cut to maxSize; a
// command whose BSON is already larger than maxSize is described, not
// converted (see ppmongomonitor.DefaultCommandMaxSize).
func commandAnnotation(e *event.CommandStartedEvent, collection string, maxSize int) string {
	if len(e.Command) > maxSize {
		return fmt.Sprintf("[MongoDB command omitted: command=%s, collection=%s, bsonSize=%d]", e.CommandName, collection, len(e.Command))
	}

	b, err := bson.MarshalExtJSON(e.Command, false, false)
	if err != nil {
		return ""
	}
	return ppmongomonitor.AbbreviateJSON(b, maxSize)
}

func collectionName(e *event.CommandStartedEvent) string {
	coll := e.Command.Lookup(e.CommandName)
	collName, _ := coll.StringValueOK()
	return collName
}

func (m *monitor) Succeeded(ctx context.Context, evt *event.CommandSucceededEvent) {
	m.Finished(ppmongomonitor.Key{ConnectionID: evt.ConnectionID, RequestID: evt.RequestID}, nil)
}

// v1 reports CommandFailedEvent.Failure as a string; v2 reports it as an error.
func (m *monitor) Failed(ctx context.Context, evt *event.CommandFailedEvent) {
	m.Finished(ppmongomonitor.Key{ConnectionID: evt.ConnectionID, RequestID: evt.RequestID}, errors.New(evt.Failure))
}

// NewMonitor returns a *event.CommandMonitor ready to instrument.
func NewMonitor() *event.CommandMonitor {
	m := &monitor{ppmongomonitor.NewMonitor()}

	return &event.CommandMonitor{
		Started:   m.Started,
		Succeeded: m.Succeeded,
		Failed:    m.Failed,
	}
}
