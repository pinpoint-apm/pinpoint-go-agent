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

type pingerDriverConn struct {
	fakeDriverConn
	err error
}

func (c *pingerDriverConn) Ping(context.Context) error { return c.err }

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

	var b bytes.Buffer
	for i, value := range values {
		assert.True(t, writeBindValue(&b, i, value, len(values)-1, 4096))
	}

	want := make([]string, len(values))
	for i, value := range values {
		want[i] = fmt.Sprint(value)
	}
	assert.Equal(t, strings.Join(want, ", "), b.String())
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
			for i := 0; i < b.N; i++ {
				var out bytes.Buffer
				writeBindValue(&out, 0, benchmark.value, 0, 1024)
			}
		})
	}
}

func Test_sqlConn_PrepareContextFallback(t *testing.T) {
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

	pingErr := errors.New("ping failed")
	assert.ErrorIs(t, newSqlConn(&pingerDriverConn{err: pingErr}, DBInfo{}).Ping(context.Background()), pingErr)

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
// wrapped connection has, and pass their answers through. A driver that marks
// a connection bad in IsValid (go-sql-driver/mysql after a busy buffer) needs
// the answer to reach database/sql, or the connection goes back to the pool.
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
			_, isExecer := conn.(driver.ExecerContext)
			assert.True(t, isExecer, "still the instrumented wrapper")
		})
	}
}

type shapeDriver struct{ conn driver.Conn }

func (d *shapeDriver) Open(string) (driver.Conn, error) { return d.conn, nil }

type shapeConnector struct{ conn driver.Conn }

func (c *shapeConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c *shapeConnector) Driver() driver.Driver                        { return nil }

// Open and Connect are the two ways database/sql obtains a connection; both
// must hand out the shape that matches the driver's connection.
func Test_sqlDriver_OpenAndConnectKeepSessionInterfaces(t *testing.T) {
	d := &sqlDriver{Driver: &shapeDriver{conn: &sessionDriverConn{valid: true}}, dbInfo: DBInfo{}}

	conn, err := d.Open("dsn")
	require.NoError(t, err)
	_, ok := conn.(driver.Validator)
	assert.True(t, ok, "Open: Validator of the wrapped connection is visible")
	_, ok = conn.(driver.SessionResetter)
	assert.True(t, ok, "Open: SessionResetter of the wrapped connection is visible")

	c := &sqlConnector{Connector: &shapeConnector{conn: &fakeDriverConn{}}, driver: d}
	conn, err = c.Connect(context.Background())
	require.NoError(t, err)
	_, ok = conn.(driver.Validator)
	assert.False(t, ok, "Connect: a driver without Validator does not gain one")
	_, ok = conn.(driver.SessionResetter)
	assert.False(t, ok, "Connect: a driver without SessionResetter does not gain one")
}

type plainDriver struct{}

func (plainDriver) Open(string) (driver.Conn, error) { return nil, errors.New("plain") }

type contextDriver struct{ plainDriver }

func (contextDriver) OpenConnector(string) (driver.Connector, error) {
	return nil, errors.New("connector")
}

// WrapSQLDriver keeps the shape of the driver it wraps: DriverContext only
// when the driver has it, so database/sql's OpenConnector path stays reachable
// for a driver that offers it and is never claimed for one that does not.
func TestWrapSQLDriverKeepsDriverContextShape(t *testing.T) {
	plain := WrapSQLDriver(plainDriver{}, DBInfo{})
	_, hasConnector := plain.(driver.DriverContext)
	assert.False(t, hasConnector, "a driver without DriverContext must not gain it")

	withContext := WrapSQLDriver(&contextDriver{}, DBInfo{})
	_, hasConnector = withContext.(driver.DriverContext)
	assert.True(t, hasConnector, "a driver with DriverContext must keep it")
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

// bindValuesOf returns the bind value string of every SQL annotation on the
// span's events, in order.
func bindValuesOf(s *span) []string {
	var binds []string
	for _, se := range s.spanEvents {
		for _, a := range se.annotations.list {
			if v := a.Value.GetIntStringStringValue(); v != nil {
				binds = append(binds, v.StringValue2.GetValue())
			}
		}
	}
	return binds
}

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
	_, ok := db.Driver().(*sqlDriver)
	assert.True(t, ok, "the connector reports the wrapping driver")

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

	var ops []string
	for _, se := range tracer.(*span).spanEvents {
		ops = append(ops, se.operationName)
		assert.Equal(t, int32(ServiceTypeMysqlExecuteQuery), se.serviceType, se.operationName)
	}
	assert.Equal(t, []string{"ConnExec", "ConnQuery", "StmtExec", "StmtQuery", "Begin", "Rollback"}, ops)
	assert.Equal(t, []string{"1", "x", "2", "3"}, bindValuesOf(tracer.(*span)), "bind values of the SQL events")
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
