package pppgxv5

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const driverName = "pgxv5-pinpoint"

type argStringer string

func (s argStringer) String() string { return "stringer:" + string(s) }

func TestWriteArgPreservesFormatting(t *testing.T) {
	values := []any{
		nil,
		"text",
		[]byte{0, 1, 127, 255},
		int64(-42),
		float64(1.25),
		true,
		time.Date(2026, time.August, 28, 1, 2, 3, 4, time.UTC),
		argStringer("value"),
	}

	var b bytes.Buffer
	for i, value := range values {
		require.True(t, writeArg(&b, i, value, len(values)-1, 4096), "writeArg stopped at value %d", i)
	}

	want := make([]string, len(values))
	for i, value := range values {
		want[i] = fmt.Sprint(value)
	}
	assert.Equal(t, strings.Join(want, ", "), b.String())
}

func TestWriteArgTruncatesOversizedValues(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
	}{
		{"string", strings.Repeat("x", 5000)},
		{"bytes", bytes.Repeat([]byte{255}, 5000)},
	} {
		t.Run(test.name, func(t *testing.T) {
			full := fmt.Sprint(test.value)
			var b bytes.Buffer
			require.False(t, writeArg(&b, 0, test.value, 0, 1024),
				"writeArg reported more values could be written")
			// The marker is written inside the limit, not past it.
			assert.Equal(t, full[:1024-len("...(1024)")]+"...(1024)", b.String())
		})
	}
}

// The separator between two values is written under the same limit as the
// values themselves, so a value landing on the boundary makes room for the
// marker instead of growing past the limit - and a zero limit keeps nothing.
func TestWriteArgTruncatesAtBoundary(t *testing.T) {
	for _, test := range []struct {
		name     string
		values   []any
		maxSize  int
		want     string
		wantMore bool
	}{
		{
			name:    "separator split by the limit",
			values:  []any{strings.Repeat("p", 1023), "z"},
			maxSize: 1024,
			want:    strings.Repeat("p", 1024-len("...(1024)")) + "...(1024)",
		},
		{
			name:     "everything fits",
			values:   []any{strings.Repeat("p", 1020), "z"},
			maxSize:  1024,
			want:     strings.Repeat("p", 1020) + ", z",
			wantMore: true,
		},
		{
			name:    "zero limit",
			values:  []any{"abc"},
			maxSize: 0,
			want:    "",
		},
		{
			name:    "a negative limit keeps nothing either",
			values:  []any{"abc"},
			maxSize: -1,
			want:    "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var b bytes.Buffer
			more := true
			for i, v := range test.values {
				if more = writeArg(&b, i, v, len(test.values)-1, test.maxSize); !more {
					break
				}
			}
			assert.Equal(t, test.wantMore, more, "writeArg reported the wrong continuation")
			assert.Equal(t, test.want, b.String())
		})
	}
}

var benchmarkArgSink string

func BenchmarkWriteArgLarge(b *testing.B) {
	for _, benchmark := range []struct {
		name  string
		value any
	}{
		{"string", strings.Repeat("x", 1<<20)},
		{"bytes", bytes.Repeat([]byte{255}, 1<<20)},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			b.SetBytes(1 << 20)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var out bytes.Buffer
				writeArg(&out, 0, benchmark.value, 0, 1024)
				benchmarkArgSink = out.String()
			}
		})
	}
}

func TestWriteArgLimitsLargeValues(t *testing.T) {
	const maxSize = 65
	tests := []struct {
		name       string
		value      any
		wantPrefix string
	}{
		{name: "string", value: strings.Repeat("가", 1<<20), wantPrefix: "가"},
		{name: "bytes", value: bytes.Repeat([]byte{255}, 1<<20), wantPrefix: "[255 "},
		{name: "slice", value: make([]int32, 1<<20), wantPrefix: "[0 0 "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			more := writeArg(&b, 0, tt.value, 0, maxSize)

			require.False(t, more, "writeArg reported that an oversized value fit")
			assert.LessOrEqual(t, b.Len(), maxSize, "the result grew past the limit")
			assert.LessOrEqual(t, b.Cap(), maxSize*2,
				"the buffer retained %d bytes for a %d-byte limit", b.Cap(), maxSize)
			assert.True(t, strings.HasPrefix(b.String(), tt.wantPrefix),
				"result %q does not preserve prefix %q", b.String(), tt.wantPrefix)
			assert.True(t, strings.HasSuffix(b.String(), "...(65)"),
				"result %q has no truncation marker", b.String())
			assert.True(t, utf8.ValidString(b.String()), "result is not valid UTF-8: %q", b.String())
		})
	}
}

func TestWriteArgLimitsMultipleValues(t *testing.T) {
	values := []any{"0123456789", "abcdefgh", "xyz"}
	var b bytes.Buffer
	for i, value := range values {
		if !writeArg(&b, i, value, len(values)-1, 20) {
			break
		}
	}

	assert.Equal(t, "0123456789, a...(20)", b.String())
	assert.Equal(t, 20, b.Len(), "the result must fill the limit exactly, not exceed it")
}

// recordingTracer captures what the pgx tracer records on a span event. A real
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

// open reports the events that were never closed; every callback pair has to
// leave this empty or the span-event stack of the surrounding request skews.
func (t *recordingTracer) open() []string {
	var open []string
	for _, e := range t.events {
		if !e.ended {
			open = append(open, e.operation)
		}
	}
	return open
}

type recordedEvent struct {
	pinpoint.SpanEventRecorder
	operation   string
	serviceType int32
	destination string
	endPoint    string
	sql         string
	sqlArgs     string
	err         error
	annotations map[int32]string
	ended       bool
}

func (e *recordedEvent) SetServiceType(typ int32)    { e.serviceType = typ }
func (e *recordedEvent) SetDestination(id string)    { e.destination = id }
func (e *recordedEvent) SetEndPoint(endPoint string) { e.endPoint = endPoint }

func (e *recordedEvent) SetSQL(sql string, args string) {
	e.sql, e.sqlArgs = sql, args
}

func (e *recordedEvent) SetError(err error, _ ...string) { e.err = err }

func (e *recordedEvent) Annotations() pinpoint.Annotation {
	return recordedAnnotation{Annotation: e.SpanEventRecorder.Annotations(), into: e.annotations}
}

type recordedAnnotation struct {
	pinpoint.Annotation
	into map[int32]string
}

func (a recordedAnnotation) AppendString(key int32, s string) { a.into[key] = s }

func startAgent(t *testing.T, opts ...pinpoint.ConfigOption) pinpoint.Agent {
	t.Helper()

	opts = append([]pinpoint.ConfigOption{
		pinpoint.WithAppName("testApp"),
		pinpoint.WithAgentId("testAgent"),
	}, opts...)

	config, err := pinpoint.NewConfig(opts...)
	require.NoError(t, err)

	agent, err := pinpoint.NewTestAgent(config, t)
	require.NoError(t, err)
	t.Cleanup(agent.Shutdown)

	return agent
}

func testConfig(t *testing.T) *pgx.ConnConfig {
	t.Helper()
	t.Setenv("PGHOST", "")
	t.Setenv("PGDATABASE", "")
	config, err := pgx.ParseConfig("postgres://testuser:p123@dbhost:5432/testdb")
	require.NoError(t, err)
	return config
}

// The endpoint recorded on every span event comes from here. pgx resolves a
// DSN against libpq's environment defaults at connect time, and both DSN
// dialects it accepts have to reduce to the same host and database, or the
// span points at a different server than the connection.
func Test_parseDSN(t *testing.T) {
	for _, tt := range []struct {
		name     string
		dsn      string
		wantHost string
		wantName string
	}{
		{
			name:     "url dsn",
			dsn:      "postgres://testuser:p123@dbhost:5432/testdb?sslmode=disable",
			wantHost: "dbhost",
			wantName: "testdb",
		},
		{
			name:     "keyword value dsn",
			dsn:      "host=dbhost port=5432 dbname=testdb user=testuser password=p123",
			wantHost: "dbhost",
			wantName: "testdb",
		},
		{
			// A socket directory is what pgx dials; it is recorded verbatim.
			name:     "unix socket directory",
			dsn:      "postgres:///testdb?host=/var/run/postgresql",
			wantHost: "/var/run/postgresql",
			wantName: "testdb",
		},
		{
			name:     "an ipv6 host",
			dsn:      "postgres://testuser@[::1]:5432/testdb",
			wantHost: "::1",
			wantName: "testdb",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PGHOST", "")
			t.Setenv("PGDATABASE", "")

			var info pinpoint.DBInfo
			parseDSN(&info, tt.dsn)

			assert.Equal(t, tt.wantHost, info.DBHost)
			assert.Equal(t, tt.wantName, info.DBName)
		})
	}
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
	assert.Equal(t, pinpoint.ServiceTypePgSql, dbInfo.DBType)
	assert.Equal(t, pinpoint.ServiceTypePgSqlExecuteQuery, dbInfo.QueryType)
	assert.NotNil(t, dbInfo.ParseDSN, "without a ParseDSN the wrapper never learns the host or database")
}

// The documented driver name is the only thing an application refers to, so it
// has to be the name package init actually registered. It is deliberately not
// plugin/pgsql's "pq-pinpoint": database/sql panics on a duplicate registration
// if a binary imports both.
func TestRegisteredDriverName(t *testing.T) {
	assert.True(t, slices.Contains(sql.Drivers(), driverName),
		"%s not registered, got %v", driverName, sql.Drivers())
}

// Opening through the registered name must hand database/sql the instrumented
// driver, not the bare stdlib one - otherwise nothing is ever traced.
func TestOpenUsesTheInstrumentedDriver(t *testing.T) {
	db, err := sql.Open(driverName, "postgres://testuser@dbhost/testdb")
	require.NoError(t, err)
	defer db.Close()

	assert.Implements(t, (*driver.Driver)(nil), db.Driver())
	assert.Implements(t, (*driver.DriverContext)(nil), db.Driver(),
		"the wrapper must keep the driver's OpenConnector reachable")
}

// Every pgx callback opens its span event through this one function, so the
// service type, endpoint and destination it sets are what the whole tracer
// records.
func Test_newSpanEvent(t *testing.T) {
	tracer := newRecordingTracer()
	newSpanEvent(pinpoint.NewContext(context.Background(), tracer), testConfig(t), "pgx.Query")

	require.Len(t, tracer.events, 1)
	e := tracer.events[0]
	assert.Equal(t, "pgx.Query", e.operation)
	assert.Equal(t, int32(pinpoint.ServiceTypePgSqlExecuteQuery), e.serviceType)
	assert.Equal(t, "dbhost", e.endPoint)
	assert.Equal(t, "testdb", e.destination)
}

// The tracer is registered on the pool, so its callbacks run for every query
// the application makes - including those from code that never started a span.
// Recording those would unbalance the span-event stack of whatever ran next on
// that goroutine.
func Test_newSpanEventIgnoresUnsampledCalls(t *testing.T) {
	config := testConfig(t)

	for _, tt := range []struct {
		name string
		ctx  context.Context
	}{
		{"background context", context.Background()},
		{"noop tracer", pinpoint.NewContext(context.Background(), pinpoint.NoopTracer())},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tracer := newSpanEvent(tt.ctx, config, "pgx.Query")
			assert.False(t, tracer.IsSampled(), "an unsampled context produced a sampled tracer")
		})
	}
}

// Connecting is a span event of its own, opened on start and closed on end -
// pgx calls the two on the same context, so an unbalanced pair would skew the
// event stack of the request that opened the connection.
func TestTraceConnect(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	pgxT := NewTracer()

	ctx = pgxT.TraceConnectStart(ctx, pgx.TraceConnectStartData{ConnConfig: testConfig(t)})
	pgxT.TraceConnectEnd(ctx, pgx.TraceConnectEndData{})

	require.Len(t, tracer.events, 1)
	e := tracer.events[0]
	assert.Equal(t, "pgx.Connect", e.operation)
	assert.Equal(t, "dbhost", e.endPoint)
	assert.Empty(t, tracer.open(), "the span event was left open")
}

// pgx hands each Start callback a live *pgx.Conn to read the connection config
// off, and there is no way to build one without a server; what those halves
// record is covered through newSpanEvent and composeArgs. The End halves take
// the connection but never use it, so the pairing they complete - and the error
// they record - is testable here.
func TestTraceQueryEnd(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	newSpanEvent(ctx, testConfig(t), "pgx.Query") // what TraceQueryStart opens

	want := errors.New("relation does not exist")
	NewTracer().TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: want})

	require.Len(t, tracer.events, 1)
	assert.Equal(t, want, tracer.events[0].err, "the query error must be recorded on the span event")
	assert.Empty(t, tracer.open(), "the span event was left open")
}

// A successful query closes its event with no error recorded.
func TestTraceQueryEnd_Success(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	newSpanEvent(ctx, testConfig(t), "pgx.Query")

	NewTracer().TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	require.Len(t, tracer.events, 1)
	assert.NoError(t, tracer.events[0].err)
	assert.Empty(t, tracer.open())
}

// The batch-level error goes on the enclosing event, which TraceBatchEnd is
// the only thing that closes.
func TestTraceBatchEnd(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	newSpanEvent(ctx, testConfig(t), "pgx.Batch") // what TraceBatchStart opens

	want := errors.New("batch aborted")
	NewTracer().TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{Err: want})

	require.Len(t, tracer.events, 1)
	assert.Equal(t, want, tracer.events[0].err)
	assert.Empty(t, tracer.open(), "the enclosing batch event was left open")
}

// CopyFrom is one event too, closed with whatever the copy failed with.
func TestTraceCopyFromEnd(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	newSpanEvent(ctx, testConfig(t), "pgx.CopyFrom") // what TraceCopyFromStart opens

	want := errors.New("copy failed")
	NewTracer().TraceCopyFromEnd(ctx, nil, pgx.TraceCopyFromEndData{Err: want})

	require.Len(t, tracer.events, 1)
	assert.Equal(t, want, tracer.events[0].err)
	assert.Empty(t, tracer.open(), "the span event was left open")
}

// The copy target is what CopyFrom records in place of a SQL statement, so the
// identifier has to reach the annotation the way pgx sanitizes it.
func TestCopyFromTargetIsSanitized(t *testing.T) {
	assert.Equal(t, `"public"."users"`, pgx.Identifier{"public", "users"}.Sanitize())
}

// Every End callback also runs for queries made outside a span. Closing a span
// event that was never opened would unwind the surrounding request's stack.
func TestEndCallbacksIgnoreUnsampledCalls(t *testing.T) {
	startAgent(t)

	pgxT := NewTracer()

	for _, ctx := range []context.Context{
		context.Background(),
		pinpoint.NewContext(context.Background(), pinpoint.NoopTracer()),
	} {
		assert.NotPanics(t, func() {
			pgxT.TraceConnectEnd(ctx, pgx.TraceConnectEndData{})
			pgxT.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
			pgxT.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{})
			pgxT.TraceCopyFromEnd(ctx, nil, pgx.TraceCopyFromEndData{})
		})
	}
}

// Bind values can hold personal data, so SQL.TraceBindValue is a privacy gate:
// with it off, nothing about the arguments may reach the span - not even how
// many there were.
func TestComposeArgs_HonoursTheBindValueGate(t *testing.T) {
	agent := startAgent(t)
	pgxT := NewTracer()

	agent.Config().Set(pinpoint.CfgSQLTraceBindValue, true)
	assert.Equal(t, "secret, 42", pgxT.composeArgs([]any{"secret", 42}))

	agent.Config().Set(pinpoint.CfgSQLTraceBindValue, false)
	assert.Empty(t, pgxT.composeArgs([]any{"secret", 42}), "bind values leaked with tracing off")
}

// A statement with no bind values has nothing to record, whatever the gate says.
func TestComposeArgs_NoArguments(t *testing.T) {
	agent := startAgent(t)
	agent.Config().Set(pinpoint.CfgSQLTraceBindValue, true)

	assert.Empty(t, NewTracer().composeArgs(nil))
	assert.Empty(t, NewTracer().composeArgs([]any{}))
}

// SQL.MaxBindValueSize bounds what one statement can add to a span, so the
// composed arguments must respect it end to end, not only inside writeArg.
func TestComposeArgs_HonoursTheSizeLimit(t *testing.T) {
	agent := startAgent(t)
	agent.Config().Set(pinpoint.CfgSQLTraceBindValue, true)
	agent.Config().Set(pinpoint.CfgSQLMaxBindValueSize, 32)

	got := NewTracer().composeArgs([]any{strings.Repeat("x", 1<<10)})

	assert.LessOrEqual(t, len(got), 32, "composeArgs grew past the configured limit")
	assert.True(t, strings.HasSuffix(got, "...(32)"), "composeArgs() = %q, want the truncation marker", got)
}
