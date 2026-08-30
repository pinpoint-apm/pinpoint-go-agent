package pppgxv5

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

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
		if !writeArg(&b, i, value, len(values)-1, 4096) {
			t.Fatalf("writeArg stopped at value %d", i)
		}
	}

	want := make([]string, len(values))
	for i, value := range values {
		want[i] = fmt.Sprint(value)
	}
	if got, want := b.String(), strings.Join(want, ", "); got != want {
		t.Fatalf("writeArg() = %q, want %q", got, want)
	}
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
			if writeArg(&b, 0, test.value, 0, 1024) {
				t.Fatal("writeArg reported more values could be written")
			}
			// The marker is written inside the limit, not past it.
			if got, want := b.String(), full[:1024-len("...(1024)")]+"...(1024)"; got != want {
				t.Fatalf("writeArg() length = %d, want %d", len(got), len(want))
			}
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
	} {
		t.Run(test.name, func(t *testing.T) {
			var b bytes.Buffer
			more := true
			for i, v := range test.values {
				if more = writeArg(&b, i, v, len(test.values)-1, test.maxSize); !more {
					break
				}
			}
			if more != test.wantMore {
				t.Fatalf("writeArg() more = %v, want %v", more, test.wantMore)
			}
			if got := b.String(); got != test.want {
				t.Fatalf("writeArg() = %q, want %q", got, test.want)
			}
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

			if more {
				t.Fatal("writeArg reported that an oversized value fit")
			}
			if b.Len() > maxSize {
				t.Fatalf("result is %d bytes; max is %d", b.Len(), maxSize)
			}
			if b.Cap() > maxSize*2 {
				t.Fatalf("buffer retained %d bytes for a %d-byte limit", b.Cap(), maxSize)
			}
			if got := b.String(); !strings.HasPrefix(got, tt.wantPrefix) {
				t.Fatalf("result %q does not preserve prefix %q", got, tt.wantPrefix)
			}
			if got := b.String(); !strings.HasSuffix(got, "...(65)") {
				t.Fatalf("result %q has no truncation marker", got)
			}
			if got := b.String(); !utf8.ValidString(got) {
				t.Fatalf("result is not valid UTF-8: %q", got)
			}
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

	if got, want := b.String(), "0123456789, a...(20)"; got != want {
		t.Fatalf("result = %q; want %q", got, want)
	}
	if b.Len() != 20 {
		t.Fatalf("result is %d bytes; want 20", b.Len())
	}
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
	ended       bool
}

func (e *recordedEvent) SetServiceType(typ int32)    { e.serviceType = typ }
func (e *recordedEvent) SetDestination(id string)    { e.destination = id }
func (e *recordedEvent) SetEndPoint(endPoint string) { e.endPoint = endPoint }

func startAgent(t *testing.T) pinpoint.Agent {
	t.Helper()
	config, err := pinpoint.NewConfig(pinpoint.WithAppName("testApp"), pinpoint.WithAgentId("testAgent"))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := pinpoint.NewTestAgent(config, t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Shutdown)
	return agent
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
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PGHOST", "")
			t.Setenv("PGDATABASE", "")

			var info pinpoint.DBInfo
			parseDSN(&info, tt.dsn)

			if info.DBHost != tt.wantHost {
				t.Errorf("DBHost = %q, want %q", info.DBHost, tt.wantHost)
			}
			if info.DBName != tt.wantName {
				t.Errorf("DBName = %q, want %q", info.DBName, tt.wantName)
			}
		})
	}
}

// An unparsable DSN must leave the driver's shared DBInfo alone rather than
// half-filling it: sql.Open reports the same error and the connection fails.
func Test_parseDSN_InvalidLeavesInfoUntouched(t *testing.T) {
	info := pinpoint.DBInfo{DBHost: "keep", DBName: "keep"}
	parseDSN(&info, "postgres://dbhost:notaport/testdb")

	if info.DBHost != "keep" || info.DBName != "keep" {
		t.Errorf("parseDSN() overwrote %q/%q", info.DBHost, info.DBName)
	}
}

// The registered driver has to carry the postgres service types; a wrong type
// files every query under the wrong node on the server map.
func TestRegisteredDriverInfo(t *testing.T) {
	if dbInfo.DBType != pinpoint.ServiceTypePgSql {
		t.Errorf("DBType = %d, want %d", dbInfo.DBType, pinpoint.ServiceTypePgSql)
	}
	if dbInfo.QueryType != pinpoint.ServiceTypePgSqlExecuteQuery {
		t.Errorf("QueryType = %d, want %d", dbInfo.QueryType, pinpoint.ServiceTypePgSqlExecuteQuery)
	}
}

// Every pgx callback opens its span event through this one function, so the
// service type, endpoint and destination it sets are what the whole tracer
// records.
func Test_newSpanEvent(t *testing.T) {
	config, err := pgx.ParseConfig("postgres://testuser:p123@dbhost:5432/testdb")
	if err != nil {
		t.Fatal(err)
	}

	tracer := newRecordingTracer()
	newSpanEvent(pinpoint.NewContext(context.Background(), tracer), config, "pgx.Query")

	if len(tracer.events) != 1 {
		t.Fatalf("recorded %d span events, want 1", len(tracer.events))
	}
	e := tracer.events[0]
	if e.operation != "pgx.Query" {
		t.Errorf("operation = %q, want %q", e.operation, "pgx.Query")
	}
	if e.serviceType != pinpoint.ServiceTypePgSqlExecuteQuery {
		t.Errorf("service type = %d, want %d", e.serviceType, pinpoint.ServiceTypePgSqlExecuteQuery)
	}
	if e.endPoint != "dbhost" {
		t.Errorf("endpoint = %q, want %q", e.endPoint, "dbhost")
	}
	if e.destination != "testdb" {
		t.Errorf("destination = %q, want %q", e.destination, "testdb")
	}
}

// The tracer is registered on the pool, so its callbacks run for every query
// the application makes - including those from code that never started a span.
// Recording those would unbalance the span-event stack of whatever ran next on
// that goroutine.
func Test_newSpanEventIgnoresUnsampledCalls(t *testing.T) {
	config, err := pgx.ParseConfig("postgres://testuser:p123@dbhost:5432/testdb")
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name string
		ctx  context.Context
	}{
		{"background context", context.Background()},
		{"noop tracer", pinpoint.NewContext(context.Background(), pinpoint.NoopTracer())},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tracer := newSpanEvent(tt.ctx, config, "pgx.Query")
			if tracer.IsSampled() {
				t.Error("an unsampled context produced a sampled tracer")
			}
		})
	}
}

// Connecting is a span event of its own, opened on start and closed on end -
// pgx calls the two on the same context, so an unbalanced pair would skew the
// event stack of the request that opened the connection.
func TestTraceConnect(t *testing.T) {
	config, err := pgx.ParseConfig("postgres://testuser:p123@dbhost:5432/testdb")
	if err != nil {
		t.Fatal(err)
	}

	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	pgxT := NewTracer()

	ctx = pgxT.TraceConnectStart(ctx, pgx.TraceConnectStartData{ConnConfig: config})
	pgxT.TraceConnectEnd(ctx, pgx.TraceConnectEndData{})

	if len(tracer.events) != 1 {
		t.Fatalf("recorded %d span events, want 1", len(tracer.events))
	}
	e := tracer.events[0]
	if e.operation != "pgx.Connect" {
		t.Errorf("operation = %q, want %q", e.operation, "pgx.Connect")
	}
	if e.endPoint != "dbhost" {
		t.Errorf("endpoint = %q, want %q", e.endPoint, "dbhost")
	}
	if !e.ended {
		t.Error("the span event was left open")
	}
}

// Bind values can hold personal data, so SQL.TraceBindValue is a privacy gate:
// with it off, nothing about the arguments may reach the span - not even how
// many there were.
func TestComposeArgs_HonoursTheBindValueGate(t *testing.T) {
	agent := startAgent(t)
	pgxT := NewTracer()

	agent.Config().Set(pinpoint.CfgSQLTraceBindValue, true)
	if got, want := pgxT.composeArgs([]any{"secret", 42}), "secret, 42"; got != want {
		t.Errorf("composeArgs() = %q, want %q", got, want)
	}

	agent.Config().Set(pinpoint.CfgSQLTraceBindValue, false)
	if got := pgxT.composeArgs([]any{"secret", 42}); got != "" {
		t.Errorf("composeArgs() = %q with tracing off, want empty", got)
	}
}

// A statement with no bind values has nothing to record, whatever the gate says.
func TestComposeArgs_NoArguments(t *testing.T) {
	agent := startAgent(t)
	agent.Config().Set(pinpoint.CfgSQLTraceBindValue, true)

	if got := NewTracer().composeArgs(nil); got != "" {
		t.Errorf("composeArgs(nil) = %q, want empty", got)
	}
}

// SQL.MaxBindValueSize bounds what one statement can add to a span, so the
// composed arguments must respect it end to end, not only inside writeArg.
func TestComposeArgs_HonoursTheSizeLimit(t *testing.T) {
	agent := startAgent(t)
	agent.Config().Set(pinpoint.CfgSQLTraceBindValue, true)
	agent.Config().Set(pinpoint.CfgSQLMaxBindValueSize, 32)

	got := NewTracer().composeArgs([]any{strings.Repeat("x", 1<<10)})

	if len(got) > 32 {
		t.Errorf("composeArgs() = %d bytes, want at most 32", len(got))
	}
	if !strings.HasSuffix(got, "...(32)") {
		t.Errorf("composeArgs() = %q, want the truncation marker", got)
	}
}
