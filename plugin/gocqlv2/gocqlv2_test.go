package ppgocqlv2

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/apache/cassandra-gocql-driver/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// v2 dropped HostInfo.SetConnectAddress in favour of this constructor.
func host(t *testing.T) *gocql.HostInfo {
	t.Helper()
	h, err := gocql.NewHostInfoFromAddrPort(net.IPv4(10, 0, 0, 1), 9042)
	require.NoError(t, err)
	return h
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

	require.Len(t, tracer.events, 1, "one query must produce exactly one span event")
	e := tracer.events[0]
	assert.Equal(t, "cassandra.query", e.operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeCassandraExecuteQuery), e.serviceType)
	assert.Equal(t, "testspace", e.destination, "the keyspace is the destination")
	assert.Equal(t, "10.0.0.1:9042", e.endPoint, "the coordinator host is the endpoint")
	assert.Equal(t, "SELECT id, text FROM widgets WHERE id = ?", e.sql)
	assert.ErrorIs(t, e.err, queryErr)
	assert.True(t, e.start.Equal(start), "start = %v, want the driver's own %v", e.start, start)
	assert.True(t, e.end.Equal(end), "end = %v, want the driver's own %v", e.end, end)
	assert.True(t, e.ended, "the span event was left open")
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

	require.Len(t, tracer.events, 1, "a batch is one round trip, so one span event")
	e := tracer.events[0]
	assert.Equal(t, "cassandra.batch", e.operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeCassandraExecuteQuery), e.serviceType)
	assert.Equal(t, "[INSERT INTO widgets (id, text) VALUES (?, ?)][DELETE FROM widgets WHERE id = ?]", e.sql)
	assert.Equal(t, "testspace", e.destination)
	assert.Equal(t, "10.0.0.1:9042", e.endPoint)
	assert.NoError(t, e.err)
	assert.True(t, e.start.Equal(start), "start = %v, want the driver's own %v", e.start, start)
	assert.True(t, e.end.Equal(end), "end = %v, want the driver's own %v", e.end, end)
	assert.True(t, e.ended, "the span event was left open")
}

// An empty batch still produces one span event, with no statements to record.
func TestObserveBatch_NoStatements(t *testing.T) {
	tracer := newRecordingTracer()

	NewObserver().ObserveBatch(pinpoint.NewContext(context.Background(), tracer), gocql.ObservedBatch{
		Keyspace: "testspace",
		Host:     host(t),
	})

	require.Len(t, tracer.events, 1)
	assert.Equal(t, "", tracer.events[0].sql, "an empty batch has no statement to record")
	assert.True(t, tracer.events[0].ended, "the span event was left open")
}

// A batch that failed records its error, so the failed round trip is the one
// that stands out in the trace.
func TestObserveBatch_Error(t *testing.T) {
	tracer := newRecordingTracer()
	want := errors.New("batch failed")

	NewObserver().ObserveBatch(pinpoint.NewContext(context.Background(), tracer), gocql.ObservedBatch{
		Keyspace:   "testspace",
		Statements: []string{"INSERT INTO widgets (id) VALUES (?)"},
		Host:       host(t),
		Err:        want,
	})

	require.Len(t, tracer.events, 1)
	assert.ErrorIs(t, tracer.events[0].err, want)
}

// A query that succeeded records no error, so a later failed one is not
// mistaken for it.
func TestObserveQuery_Success(t *testing.T) {
	tracer := newRecordingTracer()

	NewObserver().ObserveQuery(pinpoint.NewContext(context.Background(), tracer), gocql.ObservedQuery{
		Keyspace:  "testspace",
		Statement: "SELECT 1",
		Host:      host(t),
	})

	require.Len(t, tracer.events, 1)
	assert.NoError(t, tracer.events[0].err)
	assert.True(t, tracer.events[0].ended)
}

// One observer serves every query of a shared session, so a second query has
// to open its own span event rather than reuse the first one's.
func TestObserver_RecordsEveryQuery(t *testing.T) {
	tracer := newRecordingTracer()
	o := NewObserver()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	o.ObserveQuery(ctx, gocql.ObservedQuery{Statement: "SELECT 1", Host: host(t)})
	o.ObserveBatch(ctx, gocql.ObservedBatch{Statements: []string{"SELECT 2"}, Host: host(t)})
	o.ObserveQuery(ctx, gocql.ObservedQuery{Statement: "SELECT 3", Host: host(t)})

	require.Len(t, tracer.events, 3)
	assert.Equal(t, []string{"cassandra.query", "cassandra.batch", "cassandra.query"},
		[]string{tracer.events[0].operation, tracer.events[1].operation, tracer.events[2].operation})
	for _, e := range tracer.events {
		assert.True(t, e.ended, "%s was left open", e.operation)
	}
}

// The same value satisfies both of gocql's observer interfaces, which is how
// one observer instruments queries and batches alike.
func TestObserver_SatisfiesBothObserverInterfaces(t *testing.T) {
	assert.Implements(t, (*gocql.QueryObserver)(nil), NewObserver())
	assert.Implements(t, (*gocql.BatchObserver)(nil), NewObserver())
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
			assert.NotPanics(t, func() {
				o.ObserveQuery(tt.ctx, gocql.ObservedQuery{Statement: "SELECT 1"})
				o.ObserveBatch(tt.ctx, gocql.ObservedBatch{Statements: []string{"SELECT 1"}})
			}, "an untraced query must be stepped over, not recorded")
		})
	}
}
