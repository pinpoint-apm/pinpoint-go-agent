package pppgxv5

import (
	"context"
	"database/sql/driver"
	"errors"
	"net"
	"runtime"
	"testing"
	"time"
	"weak"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serveStartup plays the server side of a connection just far enough for pgx
// to finish connecting - startup, trust authentication, ready for query - and
// then drains the client until it terminates or hangs up. The callbacks under
// test read only the connection's config, so no query is ever answered.
func serveStartup(server net.Conn) {
	defer server.Close()
	be := pgproto3.NewBackend(server, server)
	for {
		msg, err := be.ReceiveStartupMessage()
		if err != nil {
			return
		}
		if _, ok := msg.(*pgproto3.SSLRequest); ok {
			if _, err := server.Write([]byte{'N'}); err != nil {
				return
			}
			continue
		}
		break
	}
	be.Send(&pgproto3.AuthenticationOk{})
	be.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: []byte{0, 0, 0, 1}})
	be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if be.Flush() != nil {
		return
	}
	for {
		msg, err := be.Receive()
		if err != nil {
			return
		}
		if _, ok := msg.(*pgproto3.Terminate); ok {
			return
		}
	}
}

// pipeConfig is testConfig dialing an in-process server instead of dbhost, so
// the connection keeps the host and database it was configured with.
func pipeConfig(t *testing.T) *pgx.ConnConfig {
	t.Helper()
	config := testConfig(t)
	config.TLSConfig = nil // sslmode=disable: the server below speaks no TLS
	config.Fallbacks = nil
	config.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	config.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go serveStartup(server)
		return client, nil
	}
	return config
}

// liveConn is a connected *pgx.Conn to dbhost/testdb, which every Start
// callback is handed by pgx to read the connection's config off.
func liveConn(t *testing.T) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, pipeConfig(t))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func assertDBEvent(t *testing.T, e *pptest.RecordedEvent, operation string) {
	t.Helper()
	assert.Equal(t, operation, e.Operation)
	assert.Equal(t, int32(pinpoint.ServiceTypePgSqlExecuteQuery), e.ServiceType)
	assert.Equal(t, "dbhost", e.EndPoint)
	assert.Equal(t, "testdb", e.Destination)
}

// A query opens its event at the start, with the statement and its bind
// values and the endpoint of the connection it ran on, and the end closes it.
func TestTraceQuery_LiveConn(t *testing.T) {
	conn := liveConn(t)
	tracer := pptest.NewRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	pgxT := NewTracer()

	ctx = pgxT.TraceQueryStart(ctx, conn, pgx.TraceQueryStartData{SQL: "SELECT $1", Args: []any{42}})
	require.Len(t, tracer.Events, 1)
	e := tracer.Events[0]
	assertDBEvent(t, e, "pgx.Query")
	assert.Equal(t, "SELECT $1", e.SQL)
	assert.False(t, e.Ended, "the event must stay open until the query ends")

	want := errors.New("query failed")
	pgxT.TraceQueryEnd(ctx, conn, pgx.TraceQueryEndData{Err: want})
	assert.Equal(t, want, e.Err)
	assert.True(t, e.Ended)
}

// A batch opens one event for itself and one per statement, each statement's
// ended as soon as it is recorded with its own error, and the batch's by
// TraceBatchEnd.
func TestTraceBatch_LiveConn(t *testing.T) {
	conn := liveConn(t)
	tracer := pptest.NewRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	pgxT := NewTracer()

	ctx = pgxT.TraceBatchStart(ctx, conn, pgx.TraceBatchStartData{})
	_, started := ctx.Value(batchEndKey{}).(*bool)
	require.True(t, started, "a sampled batch start must hand the end its flag")

	failed := errors.New("statement failed")
	pgxT.TraceBatchQuery(ctx, conn, pgx.TraceBatchQueryData{SQL: "INSERT 1"})
	pgxT.TraceBatchQuery(ctx, conn, pgx.TraceBatchQueryData{SQL: "INSERT 2", Err: failed})
	pgxT.TraceBatchEnd(ctx, conn, pgx.TraceBatchEndData{})

	require.Len(t, tracer.Events, 3)
	assertDBEvent(t, tracer.Events[0], "pgx.Batch")
	for i, want := range []struct {
		sql string
		err error
	}{{"INSERT 1", nil}, {"INSERT 2", failed}} {
		e := tracer.Events[i+1]
		assertDBEvent(t, e, "pgx.BatchQuery")
		assert.Equal(t, want.sql, e.SQL)
		assert.Equal(t, want.err, e.Err)
		assert.True(t, e.Ended)
	}
	assert.Nil(t, tracer.Events[0].Err)
	assert.True(t, tracer.Events[0].Ended)
}

// TraceCopyFromEnd records the copy whole against the connection it ran on.
func TestTraceCopyFromEnd_LiveConn(t *testing.T) {
	conn := liveConn(t)
	tracer := pptest.NewRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	pgxT := NewTracer()

	ctx = pgxT.TraceCopyFromStart(ctx, conn, pgx.TraceCopyFromStartData{TableName: pgx.Identifier{"users"}})
	want := errors.New("copy failed")
	pgxT.TraceCopyFromEnd(ctx, conn, pgx.TraceCopyFromEndData{Err: want})

	require.Len(t, tracer.Events, 1)
	e := tracer.Events[0]
	assertDBEvent(t, e, "pgx.CopyFrom")
	assert.Equal(t, `"users"`, e.Strings[pinpoint.AnnotationArgs0])
	assert.Equal(t, want, e.Err)
	assert.True(t, e.Ended)
}

// Untraced work must neither open an event nor hand the end halves anything
// that would make them close one: their callbacks run for every query made on
// an instrumented pool.
func TestCallbacks_Unsampled(t *testing.T) {
	conn := liveConn(t)
	ctx := context.Background()
	pgxT := NewTracer()

	assert.Equal(t, ctx, pgxT.TraceConnectStart(ctx, pgx.TraceConnectStartData{ConnConfig: testConfig(t)}))
	assert.Equal(t, ctx, pgxT.TraceQueryStart(ctx, conn, pgx.TraceQueryStartData{SQL: "SELECT 1"}))
	assert.Equal(t, ctx, pgxT.TraceBatchStart(ctx, conn, pgx.TraceBatchStartData{}))
	assert.Equal(t, ctx, pgxT.TraceCopyFromStart(ctx, conn, pgx.TraceCopyFromStartData{}))

	// The end halves find nothing to end and must not panic on that.
	pgxT.TraceConnectEnd(ctx, pgx.TraceConnectEndData{})
	pgxT.TraceQueryEnd(ctx, conn, pgx.TraceQueryEndData{})
	pgxT.TraceBatchQuery(ctx, conn, pgx.TraceBatchQueryData{})
	pgxT.TraceBatchEnd(ctx, conn, pgx.TraceBatchEndData{})
	pgxT.TraceCopyFromEnd(ctx, conn, pgx.TraceCopyFromEndData{})
}

// The DBInfo is built once per connection and shared by every later event on
// it: Conn.Config() is a deep copy, too costly to pay per query.
func Test_connDBInfo_CachedPerConnection(t *testing.T) {
	a, b := liveConn(t), liveConn(t)

	info := connDBInfo(a)
	assert.Equal(t, "dbhost", info.DBHost)
	assert.Equal(t, "testdb", info.DBName)
	assert.Equal(t, pinpoint.ServiceTypePgSqlExecuteQuery, info.QueryType)
	assert.Same(t, info, connDBInfo(a), "a second lookup on the same connection built a new DBInfo")
	assert.NotSame(t, info, connDBInfo(b), "two connections shared one DBInfo")
}

// The cache holds connections weakly, so a pooled connection that is closed
// and collected leaves it: pgx has no close hook to remove it by.
func Test_connDBInfo_DroppedWhenConnectionIsCollected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, pipeConfig(t))
	require.NoError(t, err)
	connDBInfo(conn)
	key := weak.Make(conn)
	_, ok := connInfos.Load(key)
	require.True(t, ok)

	require.NoError(t, conn.Close(ctx))
	conn = nil //nolint:ineffassign // drops the last strong reference

	assert.Eventually(t, func() bool {
		runtime.GC()
		_, ok := connInfos.Load(key)
		return !ok
	}, 5*time.Second, 10*time.Millisecond, "the entry of a collected connection was never dropped")
}

// A stdlib.RegisterConnConfig name cannot be parsed as a DSN, so the host and
// database are read off the pgx connection the driver opened.
func Test_parseConn_StdlibConn(t *testing.T) {
	name := stdlib.RegisterConnConfig(pipeConfig(t))
	defer stdlib.UnregisterConnConfig(name)

	conn, err := stdlib.GetDefaultDriver().Open(name)
	require.NoError(t, err)
	defer conn.Close()
	_, isStdlib := conn.(*stdlib.Conn)
	require.True(t, isStdlib)

	var info pinpoint.DBInfo
	parseConn(&info, conn.(driver.Conn))
	assert.Equal(t, "dbhost", info.DBHost)
	assert.Equal(t, "testdb", info.DBName)
}
