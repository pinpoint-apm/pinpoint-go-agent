package pinpoint

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeDriverConn struct{ begun bool }

func (c *fakeDriverConn) Prepare(query string) (driver.Stmt, error) { return nil, nil }
func (c *fakeDriverConn) Close() error                              { return nil }
func (c *fakeDriverConn) Begin() (driver.Tx, error) {
	c.begun = true
	return &fakeDriverTx{}, nil
}

type fakeDriverTx struct{ rolledBack bool }

func (t *fakeDriverTx) Commit() error   { return nil }
func (t *fakeDriverTx) Rollback() error { t.rolledBack = true; return nil }

type checkerDriverConn struct {
	fakeDriverConn
	checked bool
}

func (c *checkerDriverConn) CheckNamedValue(nv *driver.NamedValue) error {
	c.checked = true
	return nil
}

type prepareDriverConn struct {
	fakeDriverConn
	stmt      driver.Stmt
	err       error
	onPrepare func()
}

func (c *prepareDriverConn) Prepare(query string) (driver.Stmt, error) {
	if c.onPrepare != nil {
		c.onPrepare()
	}
	return c.stmt, c.err
}

type fakeDriverStmt struct{ closed bool }

func (s *fakeDriverStmt) Close() error                                    { s.closed = true; return nil }
func (s *fakeDriverStmt) NumInput() int                                   { return -1 }
func (s *fakeDriverStmt) Exec(args []driver.Value) (driver.Result, error) { return nil, nil }
func (s *fakeDriverStmt) Query(args []driver.Value) (driver.Rows, error)  { return nil, nil }

type bindStringer string

func (s bindStringer) String() string { return "stringer:" + string(s) }

func Test_writeBindValue_PreservesFormatting(t *testing.T) {
	values := []interface{}{
		nil,
		"text",
		[]byte{0, 1, 127, 255},
		int64(-42),
		float64(1.25),
		true,
		time.Date(2026, time.August, 28, 1, 2, 3, 4, time.UTC),
		bindStringer("value"),
	}

	var b strings.Builder
	for i, value := range values {
		assert.True(t, writeBindValue(&b, i, value, len(values)-1, 4096))
	}

	want := make([]string, len(values))
	for i, value := range values {
		want[i] = fmt.Sprint(value)
	}
	assert.Equal(t, strings.Join(want, ", "), b.String())
}

func Test_writeBindValue_LimitsLargeValues(t *testing.T) {
	const maxSize = 65
	tests := []struct {
		name       string
		value      interface{}
		wantPrefix string
		wantSuffix string
	}{
		// A string reports its length in bytes, an array the number of
		// StringUtils.abbreviate and ArrayUtils.abbreviate report it.
		{name: "string", value: strings.Repeat("가", 1<<20), wantPrefix: "가", wantSuffix: "...(3145728)"},
		{name: "bytes", value: bytes.Repeat([]byte{255}, 1<<20), wantPrefix: "[255 ", wantSuffix: "...(1048576)"},
		{name: "slice", value: make([]int32, 1<<20), wantPrefix: "[0 0 ", wantSuffix: "...(1048576)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			more := writeBindValue(&b, 0, tt.value, 0, maxSize)

			assert.True(t, more)
			assert.LessOrEqual(t, b.Len(), maxSize+maxBindValueMarkerSize)
			assert.LessOrEqual(t, b.Cap(), maxSize*2)
			assert.True(t, strings.HasPrefix(b.String(), tt.wantPrefix), b.String())
			assert.True(t, strings.HasSuffix(b.String(), tt.wantSuffix), b.String())
			assert.True(t, utf8.ValidString(b.String()), b.String())
		})
	}
}

func Test_writeBindValue_TruncatesOversizedBytes(t *testing.T) {
	value := bytes.Repeat([]byte{255}, 5000)
	want := fmt.Sprint(value)

	var b strings.Builder
	more := writeBindValue(&b, 0, value, 0, 1024)

	assert.True(t, more)
	// The marker counts the bytes of the slice, not the characters of its
	// rendering.
	assert.Equal(t, want[:1024]+"...(5000)", b.String())
}

// The bind value list is joined with ", " and cut at the configured limit, with
// a marker standing in for whatever the limit left out. The separator precedes
// whatever comes next, so a list cut short ends with the separator and then the
// count marker. A string value is cut where StringUtils.appendAbbreviate does,
// and its marker reports the value's length in bytes.
func Test_writeBindValue_MatchesJavaBindValueJoin(t *testing.T) {
	for _, tt := range []struct {
		name    string
		values  []interface{}
		maxSize int
		want    string
		more    bool
	}{
		{name: "at the limit", values: []interface{}{"1234"}, maxSize: 4, want: "1234", more: true},
		{name: "one byte over", values: []interface{}{"12345"}, maxSize: 4, want: "1234...(5)", more: true},
		{name: "far over the budget", values: []interface{}{strings.Repeat("v", 20)}, maxSize: 4, want: "vvvv...(20)", more: true},
		{name: "a value that fits, then one that does not", values: []interface{}{"1", strings.Repeat("z", 11)}, maxSize: 4, want: "1, zzzz...(11)", more: true},
		{name: "tail dropped after a value that fit", values: []interface{}{"1234", "5"}, maxSize: 4, want: "1234, ...(2)"},
		{name: "both markers in one list", values: []interface{}{"12345", strings.Repeat("z", 11)}, maxSize: 10, want: "12345, " + strings.Repeat("z", 10) + "...(11)", more: true},
		{name: "a CLOB at the default limit", values: []interface{}{strings.Repeat("v", 2000)}, maxSize: 1024, want: strings.Repeat("v", 1024) + "...(2000)", more: true},
		{name: "oversized value", values: []interface{}{strings.Repeat("x", 5000)}, maxSize: 1024, want: strings.Repeat("x", 1024) + "...(5000)", more: true},
		{name: "budget spent by the first value", values: []interface{}{strings.Repeat("p", 1023), "z"}, maxSize: 1024, want: strings.Repeat("p", 1023) + ", ...(2)"},
		{name: "everything fits", values: []interface{}{strings.Repeat("p", 1020), "z"}, maxSize: 1024, want: strings.Repeat("p", 1020) + ", z", more: true},
		{name: "two of three values dropped", values: []interface{}{"0123456789", "b", "c"}, maxSize: 10, want: "0123456789, ...(3)"},
		// The budget is spent between values, so "abcdefgh" goes in whole even
		// though it lands on the limit; the round after it finds nothing left.
		{name: "value landing on the limit", values: []interface{}{"0123456789", "abcdefgh", "xyz"}, maxSize: 20, want: "0123456789, abcdefgh, ...(3)"},
		// The marker counts the bind values, so it fits no limit at all -
		// appending it past the limit is what keeps the truncation visible.
		{name: "limit shorter than the marker", values: []interface{}{"a", "b", "c"}, maxSize: 2, want: "a, ...(3)"},
		{name: "tracing off", values: []interface{}{"abc"}, maxSize: 0, want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			more := true
			for i, v := range tt.values {
				if more = writeBindValue(&b, i, v, len(tt.values)-1, tt.maxSize); !more {
					break
				}
			}
			assert.Equal(t, tt.more, more)
			assert.Equal(t, tt.want, b.String())
		})
	}
}

func Benchmark_writeBindValue_Large(b *testing.B) {
	for _, benchmark := range []struct {
		name  string
		value interface{}
	}{
		{"string", strings.Repeat("x", 1<<20)},
		{"bytes", bytes.Repeat([]byte{255}, 1<<20)},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			b.SetBytes(1 << 20)
			b.ReportAllocs()
			for b.Loop() {
				var out strings.Builder
				writeBindValue(&out, 0, benchmark.value, 0, 1024)
			}
		})
	}
}

// The BeginTx fallback must mirror database/sql's own checks: the wrapper
// implements driver.ConnBeginTx, so database/sql never runs them itself.
func Test_sqlConn_BeginTxFallbackHonorsOptions(t *testing.T) {
	base := &fakeDriverConn{}
	conn := newSqlConn(base, DBInfo{})

	_, err := conn.BeginTx(context.Background(), driver.TxOptions{Isolation: driver.IsolationLevel(sql.LevelSerializable)})
	assert.Error(t, err, "non-default isolation must not be silently downgraded")
	assert.False(t, base.begun, "no transaction begun")

	_, err = conn.BeginTx(context.Background(), driver.TxOptions{ReadOnly: true})
	assert.Error(t, err, "read-only must not be silently downgraded")
	assert.False(t, base.begun, "no transaction begun")

	tx, err := conn.BeginTx(context.Background(), driver.TxOptions{})
	assert.NoError(t, err)
	assert.True(t, base.begun, "default options fall back to Begin")
	assert.NotNil(t, tx)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = conn.BeginTx(canceled, driver.TxOptions{})
	assert.ErrorIs(t, err, context.Canceled)
}

func Test_sqlConn_PrepareContextFallback(t *testing.T) {
	t.Run("canceled before Prepare", func(t *testing.T) {
		stmt := &fakeDriverStmt{}
		conn := newSqlConn(&prepareDriverConn{stmt: stmt}, DBInfo{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		got, err := conn.PrepareContext(ctx, "SELECT 1")

		assert.Nil(t, got)
		assert.ErrorIs(t, err, context.Canceled)
		assert.True(t, stmt.closed)
	})

	t.Run("canceled during Prepare", func(t *testing.T) {
		stmt := &fakeDriverStmt{}
		ctx, cancel := context.WithCancel(context.Background())
		conn := newSqlConn(&prepareDriverConn{stmt: stmt, onPrepare: cancel}, DBInfo{})

		got, err := conn.PrepareContext(ctx, "SELECT 1")

		assert.Nil(t, got)
		assert.ErrorIs(t, err, context.Canceled)
		assert.True(t, stmt.closed)
	})

	t.Run("canceled after Prepare", func(t *testing.T) {
		stmt := &fakeDriverStmt{}
		ctx, cancel := context.WithCancel(context.Background())
		conn := newSqlConn(&prepareDriverConn{stmt: stmt}, DBInfo{})

		got, err := conn.PrepareContext(ctx, "SELECT 1")
		cancel()

		assert.NoError(t, err)
		assert.NotNil(t, got)
		assert.False(t, stmt.closed)
		assert.NoError(t, got.Close())
		assert.True(t, stmt.closed)
	})

	t.Run("Prepare error", func(t *testing.T) {
		prepareErr := errors.New("prepare failed")
		conn := newSqlConn(&prepareDriverConn{err: prepareErr}, DBInfo{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		got, err := conn.PrepareContext(ctx, "SELECT 1")

		assert.Nil(t, got)
		assert.ErrorIs(t, err, prepareErr)
	})
}

// The wrapper must keep the underlying connection's optional interfaces
// working, and answer as database/sql would when they are absent.
func Test_sqlConn_OptionalInterfacePassthrough(t *testing.T) {
	plain := newSqlConn(&fakeDriverConn{}, DBInfo{})
	assert.NoError(t, plain.Ping(context.Background()), "no Pinger: succeed like database/sql")
	assert.ErrorIs(t, plain.CheckNamedValue(&driver.NamedValue{}), driver.ErrSkip, "no checker: default handling")

	checker := &checkerDriverConn{}
	conn := newSqlConn(checker, DBInfo{})
	assert.NoError(t, conn.CheckNamedValue(&driver.NamedValue{}))
	assert.True(t, checker.checked, "conn checker delegated")

	// database/sql consults only the outermost statement's checker, so the
	// statement must fall back to the connection's checker itself.
	checker.checked = false
	stmt := &sqlStmt{Stmt: &fakeDriverStmt{}, conn: conn}
	assert.NoError(t, stmt.CheckNamedValue(&driver.NamedValue{}))
	assert.True(t, checker.checked, "stmt falls back to the conn checker")

	assert.Equal(t, driver.DefaultParameterConverter,
		stmt.ColumnConverter(0), "no ColumnConverter: default converter")
}

type resetterDriverConn struct {
	fakeDriverConn
	err error
}

func (c *resetterDriverConn) ResetSession(context.Context) error { return c.err }

type validatorDriverConn struct {
	fakeDriverConn
	valid bool
}

func (c *validatorDriverConn) IsValid() bool { return c.valid }

type sessionDriverConn struct {
	resetterDriverConn
	valid bool
}

func (c *sessionDriverConn) IsValid() bool { return c.valid }

// database/sql keeps a connection after a rollback its context triggered only
// when the driver has both SessionResetter and Validator - it asks whether they
// exist, not what they answer - so the wrapper must have exactly the ones the
// wrapped connection has, and pass their answers through.
func Test_sqlConn_SessionInterfacesMatchDriver(t *testing.T) {
	for _, tt := range []struct {
		name                string
		conn                driver.Conn
		resetter, validator bool
	}{
		{"neither", &fakeDriverConn{}, false, false},
		{"SessionResetter", &resetterDriverConn{err: driver.ErrBadConn}, true, false},
		{"Validator", &validatorDriverConn{}, false, true},
		{"both", &sessionDriverConn{resetterDriverConn: resetterDriverConn{err: driver.ErrBadConn}}, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			conn := newSqlConn(tt.conn, DBInfo{}).withSessionInterfaces()

			r, isResetter := conn.(driver.SessionResetter)
			assert.Equal(t, tt.resetter, isResetter)
			if isResetter {
				assert.ErrorIs(t, r.ResetSession(context.Background()), driver.ErrBadConn, "answer passed through")
			}
			v, isValidator := conn.(driver.Validator)
			assert.Equal(t, tt.validator, isValidator)
			if isValidator {
				assert.False(t, v.IsValid(), "answer passed through")
			}
			_, isQueryer := conn.(driver.QueryerContext)
			assert.True(t, isQueryer, "still the instrumented wrapper")
		})
	}
}

func Test_sqlConn_DirectExecutionFallbackPreservesNamedArguments(t *testing.T) {
	conn := newSqlConn(&fakeDriverConn{}, DBInfo{})
	args := []driver.NamedValue{{Name: "id", Value: 1}}

	_, err := conn.ExecContext(context.Background(), "SELECT :id", args)
	assert.ErrorIs(t, err, driver.ErrSkip)
	_, err = conn.QueryContext(context.Background(), "SELECT :id", args)
	assert.ErrorIs(t, err, driver.ErrSkip)
}

// A connection must read the live config, not the one captured when it was
// opened: connections opened before NewAgent otherwise kept the noop agent's
// config forever, so no setting and no reload ever reached them.
func Test_sqlConn_UsesLiveConfig(t *testing.T) {
	conn := newSqlConn(&fakeDriverConn{}, DBInfo{})
	assert.Equal(t, GetConfig().load(), conn.cfg(), "config resolved per operation")

	c, err := NewConfig(WithAppName("TestApp"), WithSQLMaxBindValueSize(7))
	assert.NoError(t, err)
	c.offGrpc = true
	a, err := NewAgent(c)
	assert.NoError(t, err)
	defer a.Shutdown()

	assert.Equal(t, 7, conn.cfg().sqlMaxBindValueSize, "connection sees the new agent's config")

	c.Set(CfgSQLMaxBindValueSize, 9)
	assert.Equal(t, 9, conn.cfg().sqlMaxBindValueSize, "connection sees the reload")
}

// The closed set of driver.Value types is formatted straight into the buffer:
// no reflection, no fmt.Sprint string per value. Test_writeBindValue_PreservesFormatting
// pins the output; this pins the cost.
func Test_writeBindValue_DriverValuesDoNotAllocate(t *testing.T) {
	values := []interface{}{nil, int64(-42), float64(1.25), true, []byte{0, 1, 127, 255}, "text"}
	write := func(b *strings.Builder) {
		for i, value := range values {
			writeBindValue(b, i, value, len(values)-1, 1024)
		}
	}

	var b strings.Builder
	write(&b)
	assert.Equal(t, "<nil>, -42, 1.25, true, [0 1 127 255], text", b.String())

	// strings.Builder.Reset drops its buffer (the string handed out may still
	// reference it), so the runs append to one builder grown up front instead
	// of resetting it: what is measured is the writers, not the buffer.
	var runs strings.Builder
	runs.Grow(101 * (b.Len() + 1))
	allocs := testing.AllocsPerRun(100, func() { write(&runs) })
	assert.Equal(t, 0.0, allocs)
}

type plainDriver struct{}

func (plainDriver) Open(string) (driver.Conn, error) { return nil, errors.New("plain") }

type contextDriver struct{ plainDriver }

func (contextDriver) OpenConnector(string) (driver.Connector, error) {
	return nil, errors.New("connector")
}

// IsWrappedSQLDriver tells a driver WrapSQLDriver returned, in either of its
// two shapes, from a driver that has not been wrapped, so that a registration
// hook wraps once and skips a plugin's already wrapped driver.
func TestIsWrappedSQLDriver(t *testing.T) {
	assert.False(t, IsWrappedSQLDriver(plainDriver{}))
	assert.False(t, IsWrappedSQLDriver(&contextDriver{}))

	plain := WrapSQLDriver(plainDriver{}, DBInfo{})
	assert.True(t, IsWrappedSQLDriver(plain))
	_, hasConnector := plain.(driver.DriverContext)
	assert.False(t, hasConnector, "a driver without DriverContext must not gain it")

	withContext := WrapSQLDriver(&contextDriver{}, DBInfo{})
	assert.True(t, IsWrappedSQLDriver(withContext))
	_, hasConnector = withContext.(driver.DriverContext)
	assert.True(t, hasConnector, "a driver with DriverContext must keep it")

	// Wrapping twice is the mistake the check exists for; the shape still holds.
	assert.True(t, IsWrappedSQLDriver(WrapSQLDriver(plain, DBInfo{})))
}

// legacyDriver is a driver with a connector whose connections offer only the
// pre-context interfaces (Execer, Queryer, Prepare, Begin) and whose
// statements only Exec and Query: the wrapper's fallback paths.
type legacyDriver struct{}

func (legacyDriver) Open(string) (driver.Conn, error) { return &legacyConn{}, nil }
func (d legacyDriver) OpenConnector(string) (driver.Connector, error) {
	return legacyConnector{d}, nil
}

type legacyConnector struct{ d legacyDriver }

func (c legacyConnector) Connect(context.Context) (driver.Conn, error) { return &legacyConn{}, nil }
func (c legacyConnector) Driver() driver.Driver                        { return c.d }

type legacyConn struct{ fakeDriverConn }

func (c *legacyConn) Prepare(string) (driver.Stmt, error) { return &legacyStmt{}, nil }
func (c *legacyConn) Exec(string, []driver.Value) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}
func (c *legacyConn) Query(string, []driver.Value) (driver.Rows, error) { return emptyRows{}, nil }

type legacyStmt struct{ fakeDriverStmt }

func (s *legacyStmt) Exec([]driver.Value) (driver.Result, error) { return driver.RowsAffected(1), nil }
func (s *legacyStmt) Query([]driver.Value) (driver.Rows, error)  { return emptyRows{}, nil }

type emptyRows struct{}

func (emptyRows) Columns() []string         { return []string{"c"} }
func (emptyRows) Close() error              { return nil }
func (emptyRows) Next([]driver.Value) error { return io.EOF }

// A driver with only the pre-context interfaces is still traced through
// database/sql: the connector path opens instrumented connections, and the
// Exec/Query fallbacks of the connection and of a prepared statement each
// record their SQL with its bind values, as a rollback records its own event.
func Test_sqlDriver_LegacyInterfacesAreTraced(t *testing.T) {
	wrapped := WrapSQLDriver(legacyDriver{}, DBInfo{DBType: ServiceTypeMysql, QueryType: ServiceTypeMysqlExecuteQuery})
	connector, err := wrapped.(driver.DriverContext).OpenConnector("dsn")
	require.NoError(t, err)
	db := sql.OpenDB(connector)
	defer db.Close()
	assert.Same(t, wrapped.(wrappedSQLDriverContext).Driver, db.Driver(), "the connector reports the wrapping driver")

	tracer := newTestAgent(defaultConfig()).NewSpanTracer("sql", "/sql")
	ctx := NewContext(context.Background(), tracer)

	_, err = db.ExecContext(ctx, "UPDATE t SET a = ?", int64(1))
	require.NoError(t, err)
	rows, err := db.QueryContext(ctx, "SELECT ?", "x")
	require.NoError(t, err)
	require.NoError(t, rows.Close())

	stmt, err := db.PrepareContext(ctx, "INSERT INTO t VALUES (?)")
	require.NoError(t, err)
	_, err = stmt.ExecContext(ctx, int64(2))
	require.NoError(t, err)
	rows, err = stmt.QueryContext(ctx, int64(3))
	require.NoError(t, err)
	require.NoError(t, rows.Close())
	require.NoError(t, stmt.Close())

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())

	// A pre-context driver cannot take named parameters.
	_, err = db.ExecContext(ctx, "SELECT :id", sql.Named("id", 1))
	assert.ErrorContains(t, err, "Named Parameters")
	_, err = db.QueryContext(ctx, "SELECT :id", sql.Named("id", 1))
	assert.ErrorContains(t, err, "Named Parameters")

	var ops, binds []string
	for _, se := range tracer.(*span).spanEvents {
		ops = append(ops, se.operationName)
		assert.Equal(t, int32(ServiceTypeMysqlExecuteQuery), se.serviceType, se.operationName)
		if values := se.annotations.values; len(values) > 0 {
			binds = append(binds, values[0].s2)
		}
	}
	assert.Equal(t, []string{"ConnExec", "ConnQuery", "StmtExec", "StmtQuery", "Begin", "Rollback"}, ops)
	assert.Equal(t, []string{"1", "x", "2", "3"}, binds, "bind values of the SQL events")
}

// The fallbacks mirror database/sql: a context canceled before the call
// returns its error without running the statement.
func Test_sqlStmt_FallbackHonorsCanceledContext(t *testing.T) {
	conn := newSqlConn(&legacyConn{}, DBInfo{})
	stmt := &sqlStmt{Stmt: &legacyStmt{}, conn: conn, sql: "SELECT 1"}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := stmt.ExecContext(canceled, nil)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = stmt.QueryContext(canceled, nil)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = conn.ExecContext(canceled, "SELECT 1", nil)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = conn.QueryContext(canceled, "SELECT 1", nil)
	assert.ErrorIs(t, err, context.Canceled)

	named := []driver.NamedValue{{Name: "id", Value: 1}}
	_, err = stmt.ExecContext(context.Background(), named)
	assert.ErrorContains(t, err, "Named Parameters")
	_, err = stmt.QueryContext(context.Background(), named)
	assert.ErrorContains(t, err, "Named Parameters")
}
