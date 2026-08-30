package ppmongo

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/event"
)

const abbreviationMarker = "...(65536)"

var commandAnnotationSink string

func TestCommandAnnotation(t *testing.T) {
	t.Run("small command keeps extended JSON", func(t *testing.T) {
		evt := commandStartedEvent(t, "find", "widgets", strings.Repeat("x", 32))
		want, err := bson.MarshalExtJSON(evt.Command, false, false)
		if err != nil {
			t.Fatal(err)
		}

		if got := commandAnnotation(evt, "widgets"); got != string(want) {
			t.Fatalf("commandAnnotation() = %q, want %q", got, want)
		}
	})

	t.Run("expanded extended JSON is abbreviated", func(t *testing.T) {
		// Control bytes are escaped as \uXXXX, so BSON below the gate still
		// converts to extended JSON above maxJsonSize.
		evt := commandStartedEvent(t, "insert", "widgets", strings.Repeat("\x01", 60<<10))
		if len(evt.Command) > maxBsonSize {
			t.Fatalf("test command size = %d, want at most %d", len(evt.Command), maxBsonSize)
		}
		b, err := bson.MarshalExtJSON(evt.Command, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) <= maxJsonSize {
			t.Fatalf("test extended JSON size = %d, want greater than %d", len(b), maxJsonSize)
		}

		got := commandAnnotation(evt, "widgets")
		if !strings.HasSuffix(got, abbreviationMarker) {
			t.Fatalf("commandAnnotation() = %.80q, want the abbreviation marker", got)
		}
		if len(got) > maxJsonSize {
			t.Fatalf("commandAnnotation() = %d bytes, want at most %d", len(got), maxJsonSize)
		}
		if want := string(b[:maxJsonSize-len(abbreviationMarker)]) + abbreviationMarker; got != want {
			t.Fatalf("commandAnnotation() = %d bytes, want the first %d JSON bytes plus the marker", len(got), len(want)-len(abbreviationMarker))
		}
	})

	t.Run("abbreviation keeps valid UTF-8", func(t *testing.T) {
		// Escaped control bytes push the cut into the multi-byte run that follows.
		evt := commandStartedEvent(t, "insert", "widgets", strings.Repeat("\x01", 10900)+strings.Repeat("\uac00", 200))
		b, err := bson.MarshalExtJSON(evt.Command, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if cut := maxJsonSize - len(abbreviationMarker); utf8.RuneStart(b[cut]) {
			t.Fatalf("test payload does not straddle the cut at %d", cut)
		}

		got := commandAnnotation(evt, "widgets")
		if !utf8.ValidString(got) {
			t.Fatalf("commandAnnotation() is not valid UTF-8 (%d bytes)", len(got))
		}
		if !strings.HasSuffix(got, abbreviationMarker) {
			t.Fatalf("commandAnnotation() = %.80q, want the abbreviation marker", got)
		}
	})

	t.Run("large command skips extended JSON", func(t *testing.T) {
		evt := commandStartedEvent(t, "insert", "widgets", strings.Repeat("x", maxBsonSize))
		if len(evt.Command) <= maxBsonSize {
			t.Fatalf("test command size = %d, want greater than %d", len(evt.Command), maxBsonSize)
		}

		want := fmt.Sprintf("[MongoDB command omitted: command=insert, collection=widgets, bsonSize=%d]", len(evt.Command))
		if got := commandAnnotation(evt, "widgets"); got != want {
			t.Fatalf("commandAnnotation() = %q, want %q", got, want)
		}
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
	if err != nil {
		t.Fatal(err)
	}
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
		if got := getHost(tt.connID); got != tt.want {
			t.Errorf("getHost(%q) = %q, want %q", tt.connID, got, tt.want)
		}
	}
}

// The collection is the first value of the command document, keyed by the
// command name. A command that carries no collection - a database-level one -
// must not report a garbled name.
func Test_collectionName(t *testing.T) {
	if got := collectionName(commandStartedEvent(t, "find", "widgets", "x")); got != "widgets" {
		t.Errorf("collectionName() = %q, want %q", got, "widgets")
	}

	command, err := bson.Marshal(bson.D{{Key: "ping", Value: 1}})
	if err != nil {
		t.Fatal(err)
	}
	evt := &event.CommandStartedEvent{Command: command, CommandName: "ping"}
	if got := collectionName(evt); got != "" {
		t.Errorf("collectionName(database command) = %q, want empty", got)
	}
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

	if len(tracer.events) != 1 {
		t.Fatalf("recorded %d span events, want 1", len(tracer.events))
	}
	e := tracer.events[0]
	if e.operation != "mongodb.find" {
		t.Errorf("operation = %q, want %q", e.operation, "mongodb.find")
	}
	if e.serviceType != pinpoint.ServiceTypeMongoExecuteQuery {
		t.Errorf("service type = %d, want %d", e.serviceType, pinpoint.ServiceTypeMongoExecuteQuery)
	}
	if e.endPoint != "mongo1" {
		t.Errorf("endpoint = %q, want %q", e.endPoint, "mongo1")
	}
	if e.destination != "testdb" {
		t.Errorf("destination = %q, want %q", e.destination, "testdb")
	}
	if got := e.annotations[pinpoint.AnnotationMongoCollectionInfo]; got != "widgets" {
		t.Errorf("collection annotation = %q, want %q", got, "widgets")
	}
	if got := e.annotations[pinpoint.AnnotationMongoJasonData]; got == "" {
		t.Error("the command was not recorded as an annotation")
	}
	if e.err != nil {
		t.Errorf("recorded error = %v, want nil", e.err)
	}
	if !e.ended {
		t.Error("the span event was left open")
	}
	if len(m.spans) != 0 {
		t.Errorf("%d spans left in the map, want none", len(m.spans))
	}
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

	e := tracer.events[0]
	if e.err == nil || !strings.Contains(e.err.Error(), "duplicate key") {
		t.Errorf("recorded error = %v, want the driver's failure text", e.err)
	}
	if !e.ended {
		t.Error("the span event was left open")
	}
	if len(m.spans) != 0 {
		t.Errorf("%d spans left in the map, want none", len(m.spans))
	}
}

// The monitor is registered on the client, so it sees commands the driver
// issues on its own - handshakes and heartbeats on connections no application
// span ever started. Finishing one of those must not end a span event that was
// never opened.
func TestMonitor_FinishedWithoutAStart(t *testing.T) {
	m := &monitor{spans: make(map[spanKey]pinpoint.Tracer)}

	m.Succeeded(context.Background(), &event.CommandSucceededEvent{
		CommandFinishedEvent: event.CommandFinishedEvent{ConnectionID: "mongo1:27017[-3]", RequestID: 42},
	})
	m.Failed(context.Background(), &event.CommandFailedEvent{
		CommandFinishedEvent: event.CommandFinishedEvent{ConnectionID: "mongo1:27017[-3]", RequestID: 42},
		Failure:              "boom",
	})
}

// An unsampled command leaves nothing in the span map, so the matching finish
// callback has nothing to close either.
func TestMonitor_IgnoresUnsampledCommands(t *testing.T) {
	m := &monitor{spans: make(map[spanKey]pinpoint.Tracer)}

	started := startedEvent(t, "mongo1:27017[-3]", 42, "find", "widgets")
	m.Started(context.Background(), started)

	if len(m.spans) != 0 {
		t.Errorf("%d spans recorded for an unsampled command, want none", len(m.spans))
	}
	m.Succeeded(context.Background(), &event.CommandSucceededEvent{
		CommandFinishedEvent: event.CommandFinishedEvent{ConnectionID: started.ConnectionID, RequestID: started.RequestID},
	})
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
			if len(tracer.events) != 20 {
				t.Errorf("connection %d recorded %d span events, want 20", i, len(tracer.events))
			}
		}(i)
	}
	wg.Wait()

	if len(m.spans) != 0 {
		t.Errorf("%d spans left in the map, want none", len(m.spans))
	}
}
