// Package ppmongov2 instruments the mongodb/mongo-go-driver/v2 package (https://github.com/mongodb/mongo-go-driver).
//
// This package instruments the mongo-go-driver v2 calls.
// Use the NewMonitor as Monitor field of mongo-go-driver's ClientOptions.
//
//	opts := options.Client()
//	opts.Monitor = ppmongov2.NewMonitor()
//	client, err := mongo.Connect(opts)
//
// It is necessary to pass the context containing the pinpoint.Tracer to mongo.Client.
//
//	collection := client.Database("testdb").Collection("example")
//	ctx := pinpoint.NewContext(context.Background(), tracer)
//	collection.InsertOne(ctx, bson.M{"foo": "bar", "apm": "pinpoint"})
package ppmongov2

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
)

type spanKey struct {
	ConnectionID string
	RequestID    int64
}

const (
	maxJsonSize = 64 * 1024
	// MarshalExtJSON buffers the whole result, so commands whose source BSON already
	// reaches the limit are skipped instead of converted.
	maxBsonSize = maxJsonSize
)

// maxPendingSpans bounds the span map. The driver pairs every started event
// with a finished one, but a pairing that never arrives would otherwise hold
// its tracer forever; past the cap the oldest-unknown entry is evicted.
const maxPendingSpans = 4096

var errSpanEvicted = errors.New("ppmongov2: too many commands in flight, span evicted before its finished event")

type monitor struct {
	sync.Mutex
	spans map[spanKey]pinpoint.Tracer

	// pending mirrors len(spans) so Finished can skip the lock when nothing
	// is tracked - every unsampled command finishes with a guaranteed miss.
	pending atomic.Int64
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

	collection := collectionName(evt)
	a := tracer.SpanEvent().Annotations()
	a.AppendString(pinpoint.AnnotationMongoCollectionInfo, collection)
	if command := commandAnnotation(evt, collection); command != "" {
		a.AppendStringString(pinpoint.AnnotationMongoJasonData, command, "")
	}

	key := spanKey{
		ConnectionID: evt.ConnectionID,
		RequestID:    evt.RequestID,
	}

	var evicted pinpoint.Tracer
	m.Lock()
	// ponytail: arbitrary eviction at the cap; per-entry deadlines if evicted
	// spans ever show up in real traces.
	if len(m.spans) >= maxPendingSpans {
		for k, t := range m.spans {
			delete(m.spans, k)
			evicted = t
			break
		}
	}
	m.spans[key] = tracer
	m.pending.Store(int64(len(m.spans)))
	m.Unlock()

	if evicted != nil {
		evicted.SpanEvent().SetError(errSpanEvicted)
		evicted.EndSpanEvent()
	}
}

func commandAnnotation(e *event.CommandStartedEvent, collection string) string {
	if len(e.Command) > maxBsonSize {
		return fmt.Sprintf("[MongoDB command omitted: command=%s, collection=%s, bsonSize=%d]", e.CommandName, collection, len(e.Command))
	}

	b, err := bson.MarshalExtJSON(e.Command, false, false)
	if err != nil {
		return ""
	}
	return abbreviateJson(b, maxJsonSize)
}

func collectionName(e *event.CommandStartedEvent) string {
	coll := e.Command.Lookup(e.CommandName)
	collName, _ := coll.StringValueOK()
	return collName
}

func (m *monitor) Succeeded(ctx context.Context, evt *event.CommandSucceededEvent) {
	m.Finished(&evt.CommandFinishedEvent, nil)
}

// v2 reports CommandFailedEvent.Failure as an error; v1 reported it as a string.
func (m *monitor) Failed(ctx context.Context, evt *event.CommandFailedEvent) {
	m.Finished(&evt.CommandFinishedEvent, evt.Failure)
}

func (m *monitor) Finished(evt *event.CommandFinishedEvent, err error) {
	// Only sampled commands were inserted in Started; when nothing is in
	// flight - the whole workload, under a zero-hit sampling rate - skip the
	// lock instead of serializing every ack on it for a guaranteed miss.
	if m.pending.Load() == 0 {
		return
	}

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
	m.pending.Store(int64(len(m.spans)))
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

	// Reserve the marker so the annotation stays inside length, and cut back to a
	// rune start: a proto3 string field rejects invalid UTF-8 and drops the span.
	marker := "...(" + strconv.Itoa(length) + ")"
	cut := length - len(marker)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(b[cut]) {
		cut--
	}
	return string(b[:cut]) + marker
}
