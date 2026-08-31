package ppmongo

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/pinpoint-apm/pinpoint-go-agent"
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

		assert.Equal(t, string(want), commandAnnotation(evt, "widgets"))
	})

	t.Run("expanded extended JSON is abbreviated", func(t *testing.T) {
		// Control bytes are escaped as \uXXXX, so BSON below the gate still
		// converts to extended JSON above maxJsonSize.
		evt := commandStartedEvent(t, "insert", "widgets", strings.Repeat("\x01", 60<<10))
		require.LessOrEqual(t, len(evt.Command), maxBsonSize, "the test command must stay below the BSON gate")
		b, err := bson.MarshalExtJSON(evt.Command, false, false)
		require.NoError(t, err)
		require.Greater(t, len(b), maxJsonSize, "the test command must expand past the JSON limit")

		got := commandAnnotation(evt, "widgets")
		assert.True(t, strings.HasSuffix(got, abbreviationMarker),
			"commandAnnotation() = %.80q, want the abbreviation marker", got)
		assert.LessOrEqual(t, len(got), maxJsonSize, "the annotation grew past the limit")
		assert.Equal(t, string(b[:maxJsonSize-len(abbreviationMarker)])+abbreviationMarker, got)
	})

	t.Run("abbreviation keeps valid UTF-8", func(t *testing.T) {
		// Escaped control bytes push the cut into the multi-byte run that follows.
		evt := commandStartedEvent(t, "insert", "widgets", strings.Repeat("\x01", 10900)+strings.Repeat("\uac00", 200))
		b, err := bson.MarshalExtJSON(evt.Command, false, false)
		require.NoError(t, err)
		cut := maxJsonSize - len(abbreviationMarker)
		require.False(t, utf8.RuneStart(b[cut]), "the test payload does not straddle the cut at %d", cut)

		got := commandAnnotation(evt, "widgets")
		assert.True(t, utf8.ValidString(got), "commandAnnotation() is not valid UTF-8 (%d bytes)", len(got))
		assert.True(t, strings.HasSuffix(got, abbreviationMarker),
			"commandAnnotation() = %.80q, want the abbreviation marker", got)
	})

	t.Run("large command skips extended JSON", func(t *testing.T) {
		evt := commandStartedEvent(t, "insert", "widgets", strings.Repeat("x", maxBsonSize))
		require.Greater(t, len(evt.Command), maxBsonSize, "the test command must exceed the BSON gate")

		want := fmt.Sprintf("[MongoDB command omitted: command=insert, collection=widgets, bsonSize=%d]", len(evt.Command))
		assert.Equal(t, want, commandAnnotation(evt, "widgets"))
	})
}

// Allocations must stay flat once the command exceeds maxBsonSize; the 1KB case
// keeps the normal conversion path measured so a regression there still shows up.
func BenchmarkCommandAnnotation(b *testing.B) {
	for _, size := range []int{1 << 10, 128 << 10, 1 << 20, 8 << 20} {
		b.Run(fmt.Sprintf("BSON_%dKB", size>>10), func(b *testing.B) {
			evt := commandStartedEvent(b, "insert", "widgets", strings.Repeat("x", size))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				commandAnnotationSink = commandAnnotation(evt, "widgets")
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

// recordingTracer captures what the monitor records on a span event. A real
// tracer's recorders are write-only, so this stands in for one.
type recordingTracer struct {
	pinpoint.Tracer
	events []*recordedEvent
}

func newRecordingTracer() *recordingTracer {
	return &recordingTracer{Tracer: pinpoint.NoopTracer()}
}

func (t *recordingTracer) IsSampled() bool { return true }

func (t *recordingTracer) NewSpanEvent(operation string) pinpoint.Tracer {
	t.events = append(t.events, &recordedEvent{
		SpanEventRecorder: t.Tracer.SpanEvent(),
		operation:         operation,
		annotations:       map[int32]string{},
	})
	return t
}

func (t *recordingTracer) SpanEvent() pinpoint.SpanEventRecorder { return t.last() }

func (t *recordingTracer) EndSpanEvent() { t.last().ended = true }

func (t *recordingTracer) last() *recordedEvent { return t.events[len(t.events)-1] }

type recordedEvent struct {
	pinpoint.SpanEventRecorder
	operation   string
	serviceType int32
	destination string
	endPoint    string
	err         error
	annotations map[int32]string
	ended       bool
}

func (e *recordedEvent) SetServiceType(typ int32)        { e.serviceType = typ }
func (e *recordedEvent) SetDestination(id string)        { e.destination = id }
func (e *recordedEvent) SetEndPoint(endPoint string)     { e.endPoint = endPoint }
func (e *recordedEvent) SetError(err error, _ ...string) { e.err = err }

func (e *recordedEvent) Annotations() pinpoint.Annotation {
	return recordedAnnotation{Annotation: e.SpanEventRecorder.Annotations(), into: e.annotations}
}

type recordedAnnotation struct {
	pinpoint.Annotation
	into map[int32]string
}

func (a recordedAnnotation) AppendString(key int32, s string) { a.into[key] = s }
func (a recordedAnnotation) AppendStringString(key int32, s1, s2 string) {
	a.into[key] = s1
}

// The driver reports the connection as an address with a pool index appended.
// The endpoint has to be the bare host, or the same server is filed under one
// node per connection on the server map.
func Test_getHost(t *testing.T) {
	for _, tt := range []struct{ connID, want string }{
		{"localhost:27017[-13]", "localhost"},
		{"mongo1.example:27017[-1]", "mongo1.example"},
		{"localhost:27017", "localhost"},
		{"localhost", "localhost"},
		{"", ""},
	} {
		assert.Equal(t, tt.want, getHost(tt.connID), "getHost(%q)", tt.connID)
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
	m := &monitor{spans: make(map[spanKey]pinpoint.Tracer)}
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	started := startedEvent(t, "mongo1:27017[-3]", 42, "find", "widgets")
	m.Started(ctx, started)
	m.Succeeded(ctx, &event.CommandSucceededEvent{
		CommandFinishedEvent: event.CommandFinishedEvent{ConnectionID: started.ConnectionID, RequestID: started.RequestID},
	})

	require.Len(t, tracer.events, 1, "one command must produce exactly one span event")
	e := tracer.events[0]
	assert.Equal(t, "mongodb.find", e.operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeMongoExecuteQuery), e.serviceType)
	assert.Equal(t, "mongo1", e.endPoint, "the endpoint is the bare host, without the pool index")
	assert.Equal(t, "testdb", e.destination)
	assert.Equal(t, "widgets", e.annotations[pinpoint.AnnotationMongoCollectionInfo])
	assert.NotEmpty(t, e.annotations[pinpoint.AnnotationMongoJasonData],
		"the command was not recorded as an annotation")
	assert.NoError(t, e.err)
	assert.True(t, e.ended, "the span event was left open")
	assert.Empty(t, m.spans, "the finished command was left in the span map")
}

// A failed command is the one worth finding in the trace, so the driver's
// failure text has to reach the span event.
func TestMonitor_Failed(t *testing.T) {
	m := &monitor{spans: make(map[spanKey]pinpoint.Tracer)}
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	started := startedEvent(t, "mongo1:27017[-3]", 42, "insert", "widgets")
	m.Started(ctx, started)
	m.Failed(ctx, &event.CommandFailedEvent{
		CommandFinishedEvent: event.CommandFinishedEvent{ConnectionID: started.ConnectionID, RequestID: started.RequestID},
		Failure:              "E11000 duplicate key error",
	})

	require.Len(t, tracer.events, 1)
	e := tracer.events[0]
	require.Error(t, e.err, "the driver's failure was not recorded")
	assert.Contains(t, e.err.Error(), "duplicate key")
	assert.True(t, e.ended, "the span event was left open")
	assert.Empty(t, m.spans, "the failed command was left in the span map")
}

// The monitor is registered on the client, so it sees commands the driver
// issues on its own - handshakes and heartbeats on connections no application
// span ever started. Finishing one of those must not end a span event that was
// never opened.
func TestMonitor_FinishedWithoutAStart(t *testing.T) {
	m := &monitor{spans: make(map[spanKey]pinpoint.Tracer)}

	assert.NotPanics(t, func() {
		m.Succeeded(context.Background(), &event.CommandSucceededEvent{
			CommandFinishedEvent: event.CommandFinishedEvent{ConnectionID: "mongo1:27017[-3]", RequestID: 42},
		})
		m.Failed(context.Background(), &event.CommandFailedEvent{
			CommandFinishedEvent: event.CommandFinishedEvent{ConnectionID: "mongo1:27017[-3]", RequestID: 42},
			Failure:              "boom",
		})
	}, "finishing a command that was never started must be a no-op")
	assert.Empty(t, m.spans)
}

// An unsampled command leaves nothing in the span map, so the matching finish
// callback has nothing to close either.
func TestMonitor_IgnoresUnsampledCommands(t *testing.T) {
	m := &monitor{spans: make(map[spanKey]pinpoint.Tracer)}

	started := startedEvent(t, "mongo1:27017[-3]", 42, "find", "widgets")
	m.Started(context.Background(), started)

	assert.Empty(t, m.spans, "an unsampled command must not be recorded in the span map")
	assert.NotPanics(t, func() {
		m.Succeeded(context.Background(), &event.CommandSucceededEvent{
			CommandFinishedEvent: event.CommandFinishedEvent{ConnectionID: started.ConnectionID, RequestID: started.RequestID},
		})
	})
}

// Two commands in flight on the same connection are told apart by request id;
// finishing one must not close the other's span event.
func TestMonitor_InterleavedCommandsOnOneConnection(t *testing.T) {
	m := &monitor{spans: make(map[spanKey]pinpoint.Tracer)}
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	first := startedEvent(t, "mongo1:27017[-3]", 1, "find", "widgets")
	second := startedEvent(t, "mongo1:27017[-3]", 2, "insert", "gadgets")
	m.Started(ctx, first)
	m.Started(ctx, second)
	require.Len(t, m.spans, 2, "two commands in flight must hold two spans")

	m.Succeeded(ctx, &event.CommandSucceededEvent{
		CommandFinishedEvent: event.CommandFinishedEvent{ConnectionID: second.ConnectionID, RequestID: second.RequestID},
	})
	assert.Len(t, m.spans, 1, "finishing one command must leave the other in flight")

	m.Succeeded(ctx, &event.CommandSucceededEvent{
		CommandFinishedEvent: event.CommandFinishedEvent{ConnectionID: first.ConnectionID, RequestID: first.RequestID},
	})
	assert.Empty(t, m.spans)

	require.Len(t, tracer.events, 2)
	assert.Equal(t, "mongodb.find", tracer.events[0].operation)
	assert.Equal(t, "mongodb.insert", tracer.events[1].operation)
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

// abbreviateJson is what keeps a command annotation inside the limit; the
// boundary cases are where a marker longer than the limit could push it over.
func Test_abbreviateJson(t *testing.T) {
	assert.Equal(t, "abc", abbreviateJson([]byte("abc"), 10), "a short value is kept whole")
	assert.Equal(t, "abc", abbreviateJson([]byte("abc"), 3), "a value exactly at the limit is kept whole")

	got := abbreviateJson([]byte(strings.Repeat("x", 100)), 20)
	assert.Len(t, got, 20)
	assert.True(t, strings.HasSuffix(got, "...(20)"), "got %q, want the marker", got)

	// A limit shorter than the marker leaves only the marker.
	assert.Equal(t, "...(2)", abbreviateJson([]byte("abcdef"), 2))
	assert.True(t, utf8.ValidString(abbreviateJson([]byte(strings.Repeat("\uac00", 100)), 20)),
		"the cut must land on a rune start")
}

// The span map is shared by every connection in the pool, so commands in
// flight at the same time have to stay separate and the map has to survive
// concurrent access. Run under -race.
func TestMonitor_ConcurrentCommands(t *testing.T) {
	m := &monitor{spans: make(map[spanKey]pinpoint.Tracer)}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tracer := newRecordingTracer()
			ctx := pinpoint.NewContext(context.Background(), tracer)

			for j := 0; j < 20; j++ {
				started := startedEvent(t, fmt.Sprintf("mongo1:27017[-%d]", i), int64(j), "find", "widgets")
				m.Started(ctx, started)
				m.Succeeded(ctx, &event.CommandSucceededEvent{
					CommandFinishedEvent: event.CommandFinishedEvent{
						ConnectionID: started.ConnectionID, RequestID: started.RequestID,
					},
				})
			}
			assert.Len(t, tracer.events, 20, "connection %d recorded the wrong number of span events", i)
		}(i)
	}
	wg.Wait()

	assert.Empty(t, m.spans, "spans were left in the map")
}
