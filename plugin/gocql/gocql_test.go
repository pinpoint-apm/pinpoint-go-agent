package ppgocql

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func host(t *testing.T) *gocql.HostInfo {
	t.Helper()
	return (&gocql.HostInfo{}).SetConnectAddress(net.IPv4(10, 0, 0, 1))
}

// The observer runs after the driver has already timed the query, so the span
// event has to carry the driver's own start and end rather than the moment the
// callback fired, along with the statement, keyspace and coordinator host. A
// query that succeeded records no error, so a later failed one is not mistaken
// for it.
func TestObserveQuery(t *testing.T) {
	start := time.Date(2026, time.August, 30, 1, 2, 3, 0, time.UTC)
	end := start.Add(15 * time.Millisecond)

	for _, queryErr := range []error{errors.New("query failed"), nil} {
		tracer := pptest.NewRecordingTracer()
		NewObserver().ObserveQuery(pinpoint.NewContext(context.Background(), tracer), gocql.ObservedQuery{
			Keyspace:  "testspace",
			Statement: "SELECT id, text FROM widgets WHERE id = ?",
			Start:     start,
			End:       end,
			Host:      host(t),
			Err:       queryErr,
		})

		require.Len(t, tracer.Events, 1, "one query must produce exactly one span event")
		e := tracer.Events[0]
		assert.Equal(t, "cassandra.query", e.Operation)
		assert.Equal(t, int32(pinpoint.ServiceTypeCassandraExecuteQuery), e.ServiceType)
		assert.Equal(t, "testspace", e.Destination, "the keyspace is the destination")
		assert.Equal(t, "10.0.0.1:0", e.EndPoint, "the coordinator host is the endpoint")
		assert.Equal(t, "SELECT id, text FROM widgets WHERE id = ?", e.SQL)
		assert.Equal(t, queryErr, e.Err)
		assert.True(t, e.Start.Equal(start), "start = %v, want the driver's own %v", e.Start, start)
		assert.True(t, e.End.Equal(end), "end = %v, want the driver's own %v", e.End, end)
		assert.True(t, e.Ended, "the span event was left open")
	}
}

// A batch is one span event, so every statement in it has to be visible in the
// recorded SQL - bracketed, since they are separate statements.
func TestObserveBatch(t *testing.T) {
	tracer := pptest.NewRecordingTracer()
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

	require.Len(t, tracer.Events, 1, "a batch is one round trip, so one span event")
	e := tracer.Events[0]
	assert.Equal(t, "cassandra.batch", e.Operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeCassandraExecuteQuery), e.ServiceType)
	assert.Equal(t, "[INSERT INTO widgets (id, text) VALUES (?, ?)][DELETE FROM widgets WHERE id = ?]", e.SQL)
	assert.Equal(t, "testspace", e.Destination)
	assert.Equal(t, "10.0.0.1:0", e.EndPoint)
	assert.NoError(t, e.Err)
	assert.True(t, e.Start.Equal(start), "start = %v, want the driver's own %v", e.Start, start)
	assert.True(t, e.End.Equal(end), "end = %v, want the driver's own %v", e.End, end)
	assert.True(t, e.Ended, "the span event was left open")
}

// An empty batch still produces one span event, with no statements to record.
func TestObserveBatch_NoStatements(t *testing.T) {
	tracer := pptest.NewRecordingTracer()

	NewObserver().ObserveBatch(pinpoint.NewContext(context.Background(), tracer), gocql.ObservedBatch{
		Keyspace: "testspace",
		Host:     host(t),
	})

	require.Len(t, tracer.Events, 1)
	assert.Equal(t, "", tracer.Events[0].SQL, "an empty batch has no statement to record")
	assert.True(t, tracer.Events[0].Ended, "the span event was left open")
}

// A batch that failed records its error, so the failed round trip is the one
// that stands out in the trace.
func TestObserveBatch_Error(t *testing.T) {
	tracer := pptest.NewRecordingTracer()
	want := errors.New("batch failed")

	NewObserver().ObserveBatch(pinpoint.NewContext(context.Background(), tracer), gocql.ObservedBatch{
		Keyspace:   "testspace",
		Statements: []string{"INSERT INTO widgets (id) VALUES (?)"},
		Host:       host(t),
		Err:        want,
	})

	require.Len(t, tracer.Events, 1)
	assert.ErrorIs(t, tracer.Events[0].Err, want)
}

// One observer serves every query of a shared session, so a second query has
// to open its own span event rather than reuse the first one's.
func TestObserver_RecordsEveryQuery(t *testing.T) {
	tracer := pptest.NewRecordingTracer()
	o := NewObserver()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	o.ObserveQuery(ctx, gocql.ObservedQuery{Statement: "SELECT 1", Host: host(t)})
	o.ObserveBatch(ctx, gocql.ObservedBatch{Statements: []string{"SELECT 2"}, Host: host(t)})
	o.ObserveQuery(ctx, gocql.ObservedQuery{Statement: "SELECT 3", Host: host(t)})

	require.Len(t, tracer.Events, 3)
	assert.Equal(t, []string{"cassandra.query", "cassandra.batch", "cassandra.query"},
		[]string{tracer.Events[0].Operation, tracer.Events[1].Operation, tracer.Events[2].Operation})
	for _, e := range tracer.Events {
		assert.True(t, e.Ended, "%s was left open", e.Operation)
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
	ctx := context.Background()

	// A nil Host would panic if the observer got as far as recording.
	assert.NotPanics(t, func() {
		o.ObserveQuery(ctx, gocql.ObservedQuery{Statement: "SELECT 1"})
		o.ObserveBatch(ctx, gocql.ObservedBatch{Statements: []string{"SELECT 1"}})
	}, "an untraced query must be stepped over, not recorded")
}
