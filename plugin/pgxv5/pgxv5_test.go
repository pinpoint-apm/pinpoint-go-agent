package pppgxv5

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

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

// Connecting is a span event of its own, opened on start and closed on end -
// pgx calls the two on the same context, so an unbalanced pair would skew the
// event stack of the request that opened the connection.
func TestTraceConnect(t *testing.T) {
	tracer := pptest.NewRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	pgxT := NewTracer()

	ctx = pgxT.TraceConnectStart(ctx, pgx.TraceConnectStartData{ConnConfig: testConfig(t)})
	pgxT.TraceConnectEnd(ctx, pgx.TraceConnectEndData{})

	require.Len(t, tracer.Events, 1)
	e := tracer.Events[0]
	assert.Equal(t, "pgx.Connect", e.Operation)
	assert.Equal(t, "dbhost", e.EndPoint)
	assert.True(t, e.Ended, "the span event was left open")
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
		{"pgx.Batch", func(ctx context.Context, err error) { pgxT.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{Err: err}) }},
		{"pgx.CopyFrom", func(ctx context.Context, err error) {
			pgxT.TraceCopyFromEnd(ctx, nil, pgx.TraceCopyFromEndData{Err: err})
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

// The copy target is what CopyFrom records in place of a SQL statement, so the
// identifier has to reach the annotation the way pgx sanitizes it. Asserting
// pgx.Identifier.Sanitize on its own tested pgx, not the plugin.
func TestCopyFromTargetIsSanitized(t *testing.T) {
	tracer := pptest.NewRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	recordCopyFromTarget(newSpanEvent(ctx, testConfig(t), "pgx.CopyFrom"),
		pgx.Identifier{"public", "users"})

	require.Len(t, tracer.Events, 1)
	assert.Equal(t, `"public"."users"`,
		tracer.Events[0].Strings[pinpoint.AnnotationArgs0],
		"the sanitized copy target belongs in the Args0 annotation")
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
