package it

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- a driver that records nothing and connects to nothing ----------------
//
// The agent's SQL instrumentation is a driver wrapper, so the bind-value,
// truncation and metadata paths are only reachable through database/sql. This
// fake driver makes them reachable without a database.

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{}, nil }

type fakeConn struct{}

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *fakeConn) Close() error                        { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)           { return fakeTx{}, nil }

func (c *fakeConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

func (c *fakeConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &fakeRows{}, nil
}

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

type fakeRows struct{}

func (r *fakeRows) Columns() []string         { return []string{"col"} }
func (r *fakeRows) Close() error              { return nil }
func (r *fakeRows) Next([]driver.Value) error { return io.EOF }

var fakeDriverSeq int32

// registerFakeDB registers a uniquely named wrapped driver and returns a
// *sql.DB using it. database/sql panics on a duplicate driver name, so every
// call gets its own.
func registerFakeDB(t *testing.T) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("pinpoint-it-fake-%d", atomic.AddInt32(&fakeDriverSeq, 1))
	sql.Register(name, pinpoint.WrapSQLDriver(fakeDriver{}, pinpoint.DBInfo{
		DBType:    pinpoint.ServiceTypeMysql,
		QueryType: pinpoint.ServiceTypeMysqlExecuteQuery,
		DBName:    "it_test",
		DBHost:    "db.example.test:3306",
	}))
	db, err := sql.Open(name, "it-dsn")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestNormalizesSqlIntoSharedUidMetadata(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())

	const rawSQL = "SELECT * FROM orders WHERE id = 42 AND status = 'ready'"
	const normalizedSQL = "SELECT * FROM orders WHERE id = 0# AND status = '1$'"

	tracer := agent.NewSpanTracer("sql.uid", "/sql-uid")
	require.True(t, tracer.IsSampled())
	spanID := tracer.SpanId()

	// The same statement twice must reuse one cached UID and publish it once.
	for i := 0; i < 2; i++ {
		event := tracer.NewSpanEvent(fmt.Sprintf("sql.%d", i))
		event.SpanEvent().SetServiceType(pinpoint.ServiceTypeMysqlExecuteQuery)
		event.SpanEvent().SetSQL(rawSQL, "")
		tracer.EndSpanEvent()
	}
	tracer.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return len(eventsForSpan(s, spanID)) >= 2 && findSqlUidMetadata(s, normalizedSQL) != nil
	}, waitTimeout))

	s := mc.Snapshot()
	metadata := findSqlUidMetadata(s, normalizedSQL)
	require.NotNil(t, metadata)
	assert.Len(t, metadata.GetSqlUid(), 16)
	assert.Equal(t, 1, countSqlUidMetadata(s, normalizedSQL), "one cache entry, one publication")

	events := eventsForSpan(s, spanID)
	require.Len(t, events, 2)
	for _, event := range events {
		uid := findAnnotation(event.GetAnnotation(), pinpoint.AnnotationSqlUid)
		require.NotNil(t, uid)
		value := uid.GetValue().GetBytesStringStringValue()
		assert.Equal(t, metadata.GetSqlUid(), value.GetBytesValue())
		assert.Equal(t, "42,ready", value.GetStringValue1().GetValue())
		assert.Empty(t, value.GetStringValue2().GetValue())
		assert.Nil(t, findAnnotation(event.GetAnnotation(), pinpoint.AnnotationSqlId))
	}
}

func TestRegistersSqlIdMetadataWhenQueryStatsDisabled(t *testing.T) {
	cfg := defaultAgentConfig()
	cfg.sqlTraceQueryStat = false
	mc, agent := startStack(t, cfg)

	const rawSQL = "UPDATE inventory SET count = 7 WHERE sku = 'ABC-1'"
	const normalizedSQL = "UPDATE inventory SET count = 0# WHERE sku = '1$'"

	tracer := agent.NewSpanTracer("sql.id", "/sql-id")
	require.True(t, tracer.IsSampled())
	spanID := tracer.SpanId()
	event := tracer.NewSpanEvent("sql.update")
	event.SpanEvent().SetServiceType(pinpoint.ServiceTypePgSqlExecuteQuery)
	event.SpanEvent().SetSQL(rawSQL, "")
	tracer.EndSpanEvent()
	tracer.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return len(eventsForSpan(s, spanID)) >= 1 && findSqlMetadata(s, normalizedSQL) != nil
	}, waitTimeout))

	s := mc.Snapshot()
	metadata := findSqlMetadata(s, normalizedSQL)
	require.NotNil(t, metadata)
	assert.Greater(t, metadata.GetSqlId(), int32(0))

	events := eventsForSpan(s, spanID)
	require.Len(t, events, 1)
	annotation := findAnnotation(events[0].GetAnnotation(), pinpoint.AnnotationSqlId)
	require.NotNil(t, annotation)
	value := annotation.GetValue().GetIntStringStringValue()
	assert.Equal(t, metadata.GetSqlId(), value.GetIntValue())
	assert.Equal(t, "7,ABC-1", value.GetStringValue1().GetValue())
	assert.Empty(t, value.GetStringValue2().GetValue())
	assert.Nil(t, findAnnotation(events[0].GetAnnotation(), pinpoint.AnnotationSqlUid))

	// SQL-id mode never registers UID metadata.
	assert.Nil(t, findSqlUidMetadata(s, normalizedSQL))
}

func TestSerializesEveryTypedSqlBindValueOnTheWire(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())
	db := registerFakeDB(t)

	tracer := agent.NewSpanTracer("sql.typed.binds", "/sql-typed-binds")
	require.True(t, tracer.IsSampled())
	spanID := tracer.SpanId()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	const query = "INSERT INTO typed_values VALUES (?, ?, ?, ?, ?, ?, ?)"
	stamp := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	_, err := db.ExecContext(ctx, query,
		nil, "alpha", true, false, int64(-9000000000), 2.25, stamp)
	require.NoError(t, err)
	tracer.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return len(eventsForSpan(s, spanID)) >= 1 && len(s.SqlUidMetadata) > 0
	}, waitTimeout))

	events := eventsForSpan(mc.Snapshot(), spanID)
	require.Len(t, events, 1)
	assert.Equal(t, int32(pinpoint.ServiceTypeMysqlExecuteQuery), events[0].GetServiceType())
	annotation := findAnnotation(events[0].GetAnnotation(), pinpoint.AnnotationSqlUid)
	require.NotNil(t, annotation)
	value := annotation.GetValue().GetBytesStringStringValue()
	assert.Empty(t, value.GetStringValue1().GetValue())
	// database/sql normalizes bind values to the driver.Value set before the
	// agent sees them, so this is every distinguishable wire representation.
	assert.Equal(t, "<nil>, alpha, true, false, -9000000000, 2.25, "+
		stamp.String(), value.GetStringValue2().GetValue())
	assert.Equal(t, "db.example.test:3306", events[0].GetNextEvent().GetMessageEvent().GetEndPoint())
	assert.Equal(t, "it_test", events[0].GetNextEvent().GetMessageEvent().GetDestinationId())
}

func TestOmitsSensitiveSqlBindValuesFromSpanPayload(t *testing.T) {
	cfg := defaultAgentConfig()
	cfg.sqlTraceBindValue = false
	mc, agent := startStack(t, cfg)
	db := registerFakeDB(t)

	const secret = "do-not-collect-this-token"
	tracer := agent.NewSpanTracer("sql.binds.disabled", "/sql-binds-disabled")
	require.True(t, tracer.IsSampled())
	spanID := tracer.SpanId()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	_, err := db.QueryContext(ctx,
		"SELECT * FROM secrets WHERE token = ? AND tenant = ?", secret, int64(42))
	require.NoError(t, err)
	tracer.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return len(eventsForSpan(s, spanID)) >= 1
	}, waitTimeout))

	events := eventsForSpan(mc.Snapshot(), spanID)
	require.Len(t, events, 1)
	annotation := findAnnotation(events[0].GetAnnotation(), pinpoint.AnnotationSqlUid)
	require.NotNil(t, annotation)
	value := annotation.GetValue().GetBytesStringStringValue()
	assert.Empty(t, value.GetStringValue1().GetValue())
	assert.Empty(t, value.GetStringValue2().GetValue())
	assert.NotContains(t, events[0].String(), secret)
}

func TestTruncatesSqlBindArgsAtConfiguredLimit(t *testing.T) {
	// Small enough that a handful of short bind values overflows the join limit.
	cfg := defaultAgentConfig()
	cfg.sqlMaxBindValueSize = 20
	mc, agent := startStack(t, cfg)
	db := registerFakeDB(t)

	tracer := agent.NewSpanTracer("sql.bind.limit", "/sql-bind-limit")
	require.True(t, tracer.IsSampled())
	spanID := tracer.SpanId()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	_, err := db.ExecContext(ctx, "SELECT * FROM items WHERE a = ? AND b = ? AND c = ?",
		"0123456789", "abcdefgh", "xyz")
	require.NoError(t, err)
	tracer.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return len(eventsForSpan(s, spanID)) >= 1
	}, waitTimeout))

	events := eventsForSpan(mc.Snapshot(), spanID)
	require.Len(t, events, 1)
	annotation := findAnnotation(events[0].GetAnnotation(), pinpoint.AnnotationSqlUid)
	require.NotNil(t, annotation)
	// "0123456789, abcdefgh" already exceeds the 20 allowed bytes, so the join
	// stops there and appends the truncation marker.
	bound := annotation.GetValue().GetBytesStringStringValue().GetStringValue2().GetValue()
	assert.True(t, strings.HasSuffix(bound, "...(20)"), bound)
	assert.NotContains(t, bound, "xyz")
}

func TestRecordsSqlErrorAndTransactionSpanEvents(t *testing.T) {
	mc, agent := startStack(t, defaultAgentConfig())
	db := registerFakeDB(t)

	tracer := agent.NewSpanTracer("sql.tx", "/sql-tx")
	require.True(t, tracer.IsSampled())
	spanID := tracer.SpanId()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, "UPDATE ledger SET amount = ? WHERE id = ?", int64(7), int64(1))
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	tracer.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return len(eventsForSpan(s, spanID)) >= 3
	}, waitTimeout))

	events := eventsForSpan(mc.Snapshot(), spanID)
	// ConnBeginTx, the traced statement, and Commit.
	assert.GreaterOrEqual(t, len(events), 3)
	found := false
	for _, e := range events {
		if findAnnotation(e.GetAnnotation(), pinpoint.AnnotationSqlUid) != nil {
			found = true
		}
	}
	assert.True(t, found, "the statement inside the transaction must carry SQL metadata")
}
