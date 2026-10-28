package pppgxv5

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// driverName is deliberately not plugin/pgsql's "pq-pinpoint": database/sql
// panics on a duplicate registration if a binary imports both.
const driverName = "pgxv5-pinpoint"

func testConfig(t *testing.T) *pgx.ConnConfig {
	t.Helper()
	t.Setenv("PGHOST", "")
	t.Setenv("PGDATABASE", "")
	config, err := pgx.ParseConfig("postgres://testuser:p123@dbhost:5432/testdb")
	require.NoError(t, err)
	return config
}

// parseDSN copies the host and database pgconn's own parser found into the
// DBInfo every span event's endpoint comes from. pgx resolves a DSN against
// libpq's environment defaults, so those are cleared first.
func Test_parseDSN(t *testing.T) {
	t.Setenv("PGHOST", "")
	t.Setenv("PGDATABASE", "")

	var info pinpoint.DBInfo
	parseDSN(&info, "postgres://testuser:p123@dbhost:5432/testdb?sslmode=disable")

	assert.Equal(t, "dbhost", info.DBHost)
	assert.Equal(t, "testdb", info.DBName)
}

// An unparsable DSN must leave the driver's shared DBInfo alone rather than
// half-filling it: sql.Open reports the same error and the connection fails.
func Test_parseDSN_InvalidLeavesInfoUntouched(t *testing.T) {
	for _, dsn := range []string{
		"postgres://dbhost:notaport/testdb",
		"host=dbhost port=notaport",
	} {
		info := pinpoint.DBInfo{DBHost: "keep", DBName: "keep"}
		parseDSN(&info, dsn)

		assert.Equal(t, "keep", info.DBHost, "parseDSN(%q) overwrote the host", dsn)
		assert.Equal(t, "keep", info.DBName, "parseDSN(%q) overwrote the database name", dsn)
	}
}

// The registered driver has to carry the postgres service types; a wrong type
// files every query under the wrong node on the server map.
func TestRegisteredDriverInfo(t *testing.T) {
	assert.Equal(t, pinpoint.ServiceTypePgSql, DBInfo().DBType)
	assert.Equal(t, pinpoint.ServiceTypePgSqlExecuteQuery, DBInfo().QueryType)
	assert.NotNil(t, DBInfo().ParseDSN, "without a ParseDSN the wrapper never learns the host or database")
}

// Opening through the registered name must hand database/sql the instrumented
// driver, not the bare stdlib one - otherwise nothing is ever traced.
func TestOpenUsesTheInstrumentedDriver(t *testing.T) {
	db, err := sql.Open(driverName, "postgres://testuser@dbhost/testdb")
	require.NoError(t, err)
	defer db.Close()

	// *stdlib.Driver implements DriverContext on its own, so the Implements
	// check alone passed with the bare driver registered. The type assertion
	// is what rules that out.
	_, bare := db.Driver().(*stdlib.Driver)
	assert.False(t, bare, "the bare stdlib driver was registered, so nothing is traced")
	assert.Implements(t, (*driver.DriverContext)(nil), db.Driver(),
		"the wrapper must keep the driver's OpenConnector reachable")
}

// Every pgx callback opens its span event through this one function, so the
// service type, endpoint and destination it sets are what the whole tracer
// records.
func Test_newSpanEvent(t *testing.T) {
	tracer := pptest.NewRecordingTracer()
	newSpanEvent(pinpoint.NewContext(context.Background(), tracer), testConfig(t), "pgx.Query")

	require.Len(t, tracer.Events, 1)
	e := tracer.Events[0]
	assert.Equal(t, "pgx.Query", e.Operation)
	assert.Equal(t, int32(pinpoint.ServiceTypePgSqlExecuteQuery), e.ServiceType)
	assert.Equal(t, "dbhost", e.EndPoint)
	assert.Equal(t, "testdb", e.Destination)
}

// The tracer is registered on the pool, so its callbacks run for every query
// the application makes - including those from code that never started a span.
// Recording those would unbalance the span-event stack of whatever ran next on
// that goroutine. pgx's Conn.Config() deep-copies the ConnConfig, so the gate
// must also come before that copy: a zero Conn has no config, and reaching
// Config() on it panics.
func Test_newSpanEventIgnoresUnsampledCalls(t *testing.T) {
	ctx := context.Background()
	assert.False(t, newSpanEvent(ctx, testConfig(t), "pgx.Query").IsSampled(), "an unsampled context produced a sampled tracer")
	assert.False(t, connSpanEvent(ctx, &pgx.Conn{}, "pgx.Query").IsSampled())
}

// forkingTracer is a RecordingTracer whose goroutine tracers record on their
// own, as a real tracer's async spans do.
type forkingTracer struct {
	*pptest.RecordingTracer
	forks []*goroutineTracer
}

func (t *forkingTracer) NewGoroutineTracer() pinpoint.Tracer {
	fork := &goroutineTracer{RecordingTracer: pptest.NewRecordingTracer()}
	t.forks = append(t.forks, fork)
	return fork
}

type goroutineTracer struct {
	*pptest.RecordingTracer
	spanEnded bool
}

func (t *goroutineTracer) EndSpan() { t.spanEnded = true }

// pgxpool connects on puddle's constructor goroutine with the values of the
// Acquire ctx, so the connect is recorded on a goroutine tracer of its own -
// event and span both ended - and the request's tracer, busy on its own
// goroutine or long past the call after a canceled Acquire, is left alone.
func TestTraceConnect(t *testing.T) {
	tracer := &forkingTracer{RecordingTracer: pptest.NewRecordingTracer()}
	ctx := pinpoint.NewContext(context.Background(), tracer)
	pgxT := NewTracer()

	want := errors.New("connect failed")
	ctx = pgxT.TraceConnectStart(ctx, pgx.TraceConnectStartData{ConnConfig: testConfig(t)})
	pgxT.TraceConnectEnd(ctx, pgx.TraceConnectEndData{Err: want})

	assert.Empty(t, tracer.Events, "the connect was recorded on the caller's tracer")
	require.Len(t, tracer.forks, 1)
	fork := tracer.forks[0]
	require.Len(t, fork.Events, 1)
	e := fork.Events[0]
	assert.Equal(t, "pgx.Connect", e.Operation)
	assert.Equal(t, "dbhost", e.EndPoint)
	assert.Equal(t, want, e.Err)
	assert.True(t, e.Ended, "the span event was left open")
	assert.True(t, fork.spanEnded, "the goroutine tracer was never ended")
}

// pgx hands each Start callback a live *pgx.Conn to read the connection config
// off, and there is no way to build one without a server; what those halves
// record is covered through newSpanEvent. The End halves take the connection
// but never use it, so the pairing they complete - and the error they record,
// none on success - is testable here. TraceBatchEnd is the only thing that
// closes the enclosing batch event.
func TestEndCallbacks(t *testing.T) {
	pgxT := NewTracer()
	for _, tt := range []struct {
		operation string
		end       func(context.Context, error)
	}{
		{"pgx.Query", func(ctx context.Context, err error) { pgxT.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: err}) }},
		{"pgx.Batch", func(ctx context.Context, err error) {
			pgxT.TraceBatchEnd(batchStarted(ctx), nil, pgx.TraceBatchEndData{Err: err})
		}},
	} {
		t.Run(tt.operation, func(t *testing.T) {
			for _, want := range []error{errors.New(tt.operation + " failed"), nil} {
				tracer := pptest.NewRecordingTracer()
				ctx := pinpoint.NewContext(context.Background(), tracer)
				newSpanEvent(ctx, testConfig(t), tt.operation) // what the Start half opens

				tt.end(ctx, want)

				require.Len(t, tracer.Events, 1)
				assert.Equal(t, want, tracer.Events[0].Err, "the error must be recorded on the span event")
				assert.True(t, tracer.Events[0].Ended, "the span event was left open")
			}
		})
	}
}

// pgx calls TraceBatchEnd twice for a batch that fails before it is sent: from
// SendBatch, and again from the BatchResults.Close the caller must make. The
// second call must leave the caller's own event open and free of the error.
func TestTraceBatchEnd_SecondCallIsIgnored(t *testing.T) {
	tracer := pptest.NewRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	tracer.NewSpanEvent("caller")
	newSpanEvent(ctx, testConfig(t), "pgx.Batch") // what TraceBatchStart opens
	ctx = batchStarted(ctx)

	pgxT := NewTracer()
	want := errors.New("prepare failed")
	pgxT.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{Err: want})
	pgxT.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{Err: want})

	require.Len(t, tracer.Events, 2)
	assert.True(t, tracer.Events[1].Ended, "the batch event was left open")
	assert.Equal(t, want, tracer.Events[1].Err)
	assert.False(t, tracer.Events[0].Ended, "the second TraceBatchEnd ended the caller's event")
	assert.Nil(t, tracer.Events[0].Err, "the batch error landed on the caller's event")
}

// CopyFrom returns without TraceCopyFromEnd when it cannot describe the target
// table, so its start opens nothing: the event is recorded whole at the end,
// starting when the copy started, with the copy target pgx sanitizes in place
// of a SQL statement. Asserting pgx.Identifier.Sanitize on its own tested pgx,
// not the plugin.
func TestTraceCopyFrom(t *testing.T) {
	tracer := pptest.NewRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	before := time.Now()
	ctx = NewTracer().TraceCopyFromStart(ctx, nil, pgx.TraceCopyFromStartData{TableName: pgx.Identifier{"public", "users"}})
	assert.Empty(t, tracer.Events, "the start opened an event a failed describe leaves open")

	start, ok := ctx.Value(copyFromKey{}).(*copyFromStart)
	require.True(t, ok, "the start handed the end nothing to record")
	want := errors.New("copy failed")
	recordCopyFrom(newSpanEvent(ctx, testConfig(t), "pgx.CopyFrom"), start, want) // what the end does

	require.Len(t, tracer.Events, 1)
	e := tracer.Events[0]
	assert.Equal(t, `"public"."users"`, e.Strings[pinpoint.AnnotationArgs0],
		"the sanitized copy target belongs in the Args0 annotation")
	assert.Equal(t, want, e.Err)
	assert.False(t, e.Start.Before(before), "the event does not start when the copy did")
	assert.False(t, e.End.Before(e.Start))
	assert.True(t, e.Ended, "the span event was left open")
}

// stdlib.RegisterConnConfig registers a ConnConfig with pgx's own driver: the
// name it returns has to open through this package's driver too, where a
// fresh stdlib.Driver parsed it as a DSN and failed every connect.
func TestOpenRegisteredConnConfig(t *testing.T) {
	config, err := pgx.ParseConfig("postgres://testuser@127.0.0.1:1/testdb?connect_timeout=1")
	require.NoError(t, err)
	name := stdlib.RegisterConnConfig(config)
	defer stdlib.UnregisterConnConfig(name)

	db, err := sql.Open(driverName, name)
	require.NoError(t, err)
	defer db.Close()

	err = db.PingContext(context.Background())
	require.Error(t, err, "nothing listens on port 1")
	assert.NotContains(t, err.Error(), "cannot parse", "the registered config was not found")
}

// Bind values can hold personal data, so SQL.TraceBindValue is a privacy gate:
// with it off, nothing about the arguments may reach the span - not even how
// many there were.
func TestComposeArgs_HonoursTheBindValueGate(t *testing.T) {
	agent := pptest.StartAgent(t)
	pgxT := NewTracer()

	agent.Config().Set(pinpoint.CfgSQLTraceBindValue, true)
	assert.Equal(t, "secret, 42", pgxT.composeArgs([]any{"secret", 42}))

	agent.Config().Set(pinpoint.CfgSQLTraceBindValue, false)
	assert.Empty(t, pgxT.composeArgs([]any{"secret", 42}), "bind values leaked with tracing off")
}

// A statement with no bind values has nothing to record, whatever the gate says.
func TestComposeArgs_NoArguments(t *testing.T) {
	agent := pptest.StartAgent(t)
	agent.Config().Set(pinpoint.CfgSQLTraceBindValue, true)

	assert.Empty(t, NewTracer().composeArgs(nil))
	assert.Empty(t, NewTracer().composeArgs([]any{}))
}

// SQL.MaxBindValueSize bounds what one statement can add to a span, so the
// composed argument list must respect it end to end, not only inside writeArg.
func TestComposeArgs_HonoursTheSizeLimit(t *testing.T) {
	agent := pptest.StartAgent(t)
	agent.Config().Set(pinpoint.CfgSQLTraceBindValue, true)
	agent.Config().Set(pinpoint.CfgSQLMaxBindValueSize, 32)

	got := NewTracer().composeArgs([]any{strings.Repeat("x", 1<<10)})

	assert.LessOrEqual(t, len(got), 32+len("...(1024)"), "composeArgs grew past the configured limit")
	assert.True(t, strings.HasSuffix(got, "...(1024)"), "composeArgs() = %q, want the truncation marker", got)
}
