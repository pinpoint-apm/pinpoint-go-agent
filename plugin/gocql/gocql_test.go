package ppgocql

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// recordingTracer captures what the observer records on a span event. A real
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
	sql         string
	err         error
	start, end  time.Time
	ended       bool
}

func (e *recordedEvent) SetServiceType(typ int32)         { e.serviceType = typ }
func (e *recordedEvent) SetDestination(id string)         { e.destination = id }
func (e *recordedEvent) SetEndPoint(endPoint string)      { e.endPoint = endPoint }
func (e *recordedEvent) SetSQL(sql string, args string)   { e.sql = sql }
func (e *recordedEvent) SetError(err error, _ ...string)  { e.err = err }
func (e *recordedEvent) FixDuration(start, end time.Time) { e.start, e.end = start, end }

func host(t *testing.T) *gocql.HostInfo {
	t.Helper()
	return (&gocql.HostInfo{}).SetConnectAddress(net.IPv4(10, 0, 0, 1))
}

// The observer runs after the driver has already timed the query, so the span
// event has to carry the driver's own start and end rather than the moment the
// callback fired, along with the statement, keyspace and coordinator host.
func TestObserveQuery(t *testing.T) {
	tracer := newRecordingTracer()
	start := time.Date(2026, time.August, 30, 1, 2, 3, 0, time.UTC)
	end := start.Add(15 * time.Millisecond)
	queryErr := errors.New("query failed")

	NewObserver().ObserveQuery(pinpoint.NewContext(context.Background(), tracer), gocql.ObservedQuery{
		Keyspace:  "testspace",
		Statement: "SELECT id, text FROM widgets WHERE id = ?",
		Start:     start,
		End:       end,
		Host:      host(t),
		Err:       queryErr,
	})

	if len(tracer.events) != 1 {
		t.Fatalf("recorded %d span events, want 1", len(tracer.events))
	}
	e := tracer.events[0]
	if e.operation != "cassandra.query" {
		t.Errorf("operation = %q, want %q", e.operation, "cassandra.query")
	}
	if e.serviceType != pinpoint.ServiceTypeCassandraExecuteQuery {
		t.Errorf("service type = %d, want %d", e.serviceType, pinpoint.ServiceTypeCassandraExecuteQuery)
	}
	if e.destination != "testspace" {
		t.Errorf("destination = %q, want %q", e.destination, "testspace")
	}
	if e.endPoint != "10.0.0.1:0" {
		t.Errorf("endpoint = %q, want %q", e.endPoint, "10.0.0.1:0")
	}
	if e.sql != "SELECT id, text FROM widgets WHERE id = ?" {
		t.Errorf("sql = %q", e.sql)
	}
	if !errors.Is(e.err, queryErr) {
		t.Errorf("error = %v, want %v", e.err, queryErr)
	}
	if !e.start.Equal(start) || !e.end.Equal(end) {
		t.Errorf("duration = %v..%v, want %v..%v", e.start, e.end, start, end)
	}
	if !e.ended {
		t.Error("the span event was left open")
	}
}

// A batch is one span event, so every statement in it has to be visible in the
// recorded SQL - bracketed, since they are separate statements.
func TestObserveBatch(t *testing.T) {
	tracer := newRecordingTracer()
	start := time.Date(2026, time.August, 30, 1, 2, 3, 0, time.UTC)
	end := start.Add(20 * time.Millisecond)

	NewObserver().ObserveBatch(pinpoint.NewContext(context.Background(), tracer), gocql.ObservedBatch{
		Keyspace: "testspace",
		Statements: []string{
			"INSERT INTO widgets (id, text) VALUES (?, ?)",
			"DELETE FROM widgets WHERE id = ?",
		},
		Start: start,
		End:   end,
		Host:  host(t),
	})

	if len(tracer.events) != 1 {
		t.Fatalf("recorded %d span events, want 1", len(tracer.events))
	}
	e := tracer.events[0]
	if e.operation != "cassandra.batch" {
		t.Errorf("operation = %q, want %q", e.operation, "cassandra.batch")
	}
	want := "[INSERT INTO widgets (id, text) VALUES (?, ?)][DELETE FROM widgets WHERE id = ?]"
	if e.sql != want {
		t.Errorf("sql = %q, want %q", e.sql, want)
	}
	if e.destination != "testspace" {
		t.Errorf("destination = %q, want %q", e.destination, "testspace")
	}
	if e.err != nil {
		t.Errorf("error = %v, want nil", e.err)
	}
	if !e.ended {
		t.Error("the span event was left open")
	}
}

// An empty batch still produces one span event, with no statements to record.
func TestObserveBatch_NoStatements(t *testing.T) {
	tracer := newRecordingTracer()

	NewObserver().ObserveBatch(pinpoint.NewContext(context.Background(), tracer), gocql.ObservedBatch{
		Keyspace: "testspace",
		Host:     host(t),
	})

	if len(tracer.events) != 1 {
		t.Fatalf("recorded %d span events, want 1", len(tracer.events))
	}
	if got := tracer.events[0].sql; got != "" {
		t.Errorf("sql = %q, want empty", got)
	}
}

// The observer is registered on the cluster, so it runs for every query the
// session makes - including those from application code that never started a
// span. Recording those would unbalance the span-event stack of whatever ran
// next on that goroutine.
func TestObserver_IgnoresUnsampledQueries(t *testing.T) {
	o := NewObserver()

	for _, tt := range []struct {
		name string
		ctx  context.Context
	}{
		{"background context", context.Background()},
		{"noop tracer", pinpoint.NewContext(context.Background(), pinpoint.NoopTracer())},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// A nil Host would panic if the observer got as far as recording.
			o.ObserveQuery(tt.ctx, gocql.ObservedQuery{Statement: "SELECT 1"})
			o.ObserveBatch(tt.ctx, gocql.ObservedBatch{Statements: []string{"SELECT 1"}})
		})
	}
}
