package ppmongo

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/mongomonitor/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/event"
)

const abbreviationMarker = "...(65536)"

var commandAnnotationSink string

func TestCommandAnnotation(t *testing.T) {
	t.Run("small command keeps extended JSON", func(t *testing.T) {
		evt := commandStartedEvent(t, "find", "widgets", strings.Repeat("x", 32))
		want, err := bson.MarshalExtJSON(evt.Command, false, false)
		require.NoError(t, err)

		assert.Equal(t, string(want), commandAnnotation(evt, "widgets", ppmongomonitor.DefaultCommandMaxSize))
	})

	t.Run("expanded extended JSON is abbreviated", func(t *testing.T) {
		// Control bytes are escaped as \uXXXX, so BSON below the gate still
		// converts to extended JSON above ppmongomonitor.DefaultCommandMaxSize.
		evt := commandStartedEvent(t, "insert", "widgets", strings.Repeat("\x01", 60<<10))
		require.LessOrEqual(t, len(evt.Command), ppmongomonitor.DefaultCommandMaxSize, "the test command must stay below the BSON gate")
		b, err := bson.MarshalExtJSON(evt.Command, false, false)
		require.NoError(t, err)
		require.Greater(t, len(b), ppmongomonitor.DefaultCommandMaxSize, "the test command must expand past the JSON limit")

		got := commandAnnotation(evt, "widgets", ppmongomonitor.DefaultCommandMaxSize)
		assert.True(t, strings.HasSuffix(got, abbreviationMarker),
			"commandAnnotation() = %.80q, want the abbreviation marker", got)
		assert.LessOrEqual(t, len(got), ppmongomonitor.DefaultCommandMaxSize, "the annotation grew past the limit")
		assert.Equal(t, string(b[:ppmongomonitor.DefaultCommandMaxSize-len(abbreviationMarker)])+abbreviationMarker, got)
	})

	t.Run("large command skips extended JSON", func(t *testing.T) {
		evt := commandStartedEvent(t, "insert", "widgets", strings.Repeat("x", ppmongomonitor.DefaultCommandMaxSize))
		require.Greater(t, len(evt.Command), ppmongomonitor.DefaultCommandMaxSize, "the test command must exceed the BSON gate")

		want := fmt.Sprintf("[MongoDB command omitted: command=insert, collection=widgets, bsonSize=%d]", len(evt.Command))
		assert.Equal(t, want, commandAnnotation(evt, "widgets", ppmongomonitor.DefaultCommandMaxSize))
	})
}

func BenchmarkCommandAnnotation(b *testing.B) {
	for _, size := range []int{1 << 10, 128 << 10, 1 << 20, 8 << 20} {
		b.Run(fmt.Sprintf("BSON_%dKB", size>>10), func(b *testing.B) {
			evt := commandStartedEvent(b, "insert", "widgets", strings.Repeat("x", size))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				commandAnnotationSink = commandAnnotation(evt, "widgets", ppmongomonitor.DefaultCommandMaxSize)
			}
		})
	}
}

func commandStartedEvent(t testing.TB, name, collection, payload string) *event.CommandStartedEvent {
	t.Helper()
	command, err := bson.Marshal(bson.D{
		{Key: name, Value: collection},
		{Key: "payload", Value: payload},
	})
	require.NoError(t, err)
	return &event.CommandStartedEvent{
		Command:     command,
		CommandName: name,
	}
}

// The collection is the first value of the command document, keyed by the
// command name. A command that carries no collection - a database-level one -
// must not report a garbled name.
func Test_collectionName(t *testing.T) {
	assert.Equal(t, "widgets", collectionName(commandStartedEvent(t, "find", "widgets", "x")))

	command, err := bson.Marshal(bson.D{{Key: "ping", Value: 1}})
	require.NoError(t, err)
	evt := &event.CommandStartedEvent{Command: command, CommandName: "ping"}
	assert.Equal(t, "", collectionName(evt), "a database-level command names no collection")
}

func startedEvent(t *testing.T, connID string, requestID int64, name, collection string) *event.CommandStartedEvent {
	t.Helper()
	evt := commandStartedEvent(t, name, collection, "payload")
	evt.ConnectionID = connID
	evt.RequestID = requestID
	evt.DatabaseName = "testdb"
	return evt
}

// One command is one span event, opened by Started and closed by the matching
// Succeeded - the driver reports them on different callbacks, so the monitor
// has to pair them by connection and request id.
func TestMonitor_StartedAndSucceeded(t *testing.T) {
	m := &monitor{ppmongomonitor.NewMonitor()}
	tracer := pptest.NewRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	started := startedEvent(t, "mongo1:27017[-3]", 42, "find", "widgets")
	m.Started(ctx, started)
	m.Succeeded(ctx, &event.CommandSucceededEvent{
		CommandFinishedEvent: event.CommandFinishedEvent{ConnectionID: started.ConnectionID, RequestID: started.RequestID},
	})

	require.Len(t, tracer.Events, 1, "one command must produce exactly one span event")
	e := tracer.Events[0]
	assert.Equal(t, "mongodb.find", e.Operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeMongoExecuteQuery), e.ServiceType)
	assert.Equal(t, "mongo1", e.EndPoint, "the endpoint is the bare host, without the pool index")
	assert.Equal(t, "testdb", e.Destination)
	assert.Equal(t, "widgets", e.Strings[pinpoint.AnnotationMongoCollectionInfo])
	assert.NotEmpty(t, e.Strings[pinpoint.AnnotationMongoJasonData],
		"the command was not recorded as an annotation")
	assert.NoError(t, e.Err)
	assert.True(t, e.Ended, "the span event was left open")
	assert.Zero(t, m.Pending(), "the finished command was left in the span map")
}

// A failed command is the one worth finding in the trace, so the driver's
// failure text has to reach the span event.
func TestMonitor_Failed(t *testing.T) {
	m := &monitor{ppmongomonitor.NewMonitor()}
	tracer := pptest.NewRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	started := startedEvent(t, "mongo1:27017[-3]", 42, "insert", "widgets")
	m.Started(ctx, started)
	m.Failed(ctx, &event.CommandFailedEvent{
		CommandFinishedEvent: event.CommandFinishedEvent{ConnectionID: started.ConnectionID, RequestID: started.RequestID},
		Failure:              "E11000 duplicate key error",
	})

	require.Len(t, tracer.Events, 1)
	e := tracer.Events[0]
	require.Error(t, e.Err, "the driver's failure was not recorded")
	assert.Contains(t, e.Err.Error(), "duplicate key")
	assert.True(t, e.Ended, "the span event was left open")
	assert.Zero(t, m.Pending(), "the failed command was left in the span map")
}

// The monitor is registered on the client, so it sees commands the driver
// issues on its own - handshakes and heartbeats on connections no application
// span ever started - and commands made outside a span. Neither leaves anything
// in the span map, so the matching finish callback must not end a span event
// that was never opened.
func TestMonitor_IgnoresUnsampledCommands(t *testing.T) {
	m := &monitor{ppmongomonitor.NewMonitor()}

	started := startedEvent(t, "mongo1:27017[-3]", 42, "find", "widgets")
	m.Started(context.Background(), started)
	assert.Zero(t, m.Pending(), "an unsampled command must not be recorded in the span map")

	assert.NotPanics(t, func() {
		m.Succeeded(context.Background(), &event.CommandSucceededEvent{
			CommandFinishedEvent: event.CommandFinishedEvent{ConnectionID: started.ConnectionID, RequestID: started.RequestID},
		})
		m.Failed(context.Background(), &event.CommandFailedEvent{
			CommandFinishedEvent: event.CommandFinishedEvent{ConnectionID: started.ConnectionID, RequestID: started.RequestID},
			Failure:              "boom",
		})
	}, "finishing a command that was never started must be a no-op")
	assert.Zero(t, m.Pending())
}

// NewMonitor is what an application installs on its client options; all three
// callbacks have to be wired, or commands go untraced or never close.
func TestNewMonitor(t *testing.T) {
	m := NewMonitor()

	require.NotNil(t, m)
	assert.NotNil(t, m.Started, "commands would go untraced")
	assert.NotNil(t, m.Succeeded, "span events would never close")
	assert.NotNil(t, m.Failed, "failures would never close their span event")
}
