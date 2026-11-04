package pinpoint

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contextDriverFull is a driver whose connections and statements implement
// every context interface database/sql looks for - the counterpart of
// legacyDriver, driving the wrapper's delegating paths rather than its
// fallbacks.
type contextDriverFull struct{ conn *contextConn }

func (d contextDriverFull) Open(string) (driver.Conn, error) { return d.conn, nil }
func (d contextDriverFull) OpenConnector(string) (driver.Connector, error) {
	return contextConnector{d}, nil
}

type contextConnector struct{ d contextDriverFull }

func (c contextConnector) Connect(context.Context) (driver.Conn, error) { return c.d.conn, nil }
func (c contextConnector) Driver() driver.Driver                        { return c.d }

type contextConn struct {
	fakeDriverConn
	pinged   bool
	txOpts   driver.TxOptions
	stmt     *contextStmt
	prepared string
}

func (c *contextConn) Ping(context.Context) error { c.pinged = true; return nil }
func (c *contextConn) PrepareContext(_ context.Context, query string) (driver.Stmt, error) {
	c.prepared = query
	return c.stmt, nil
}
func (c *contextConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.txOpts = opts
	return &fakeDriverTx{}, nil
}
func (c *contextConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}
func (c *contextConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return emptyRows{}, nil
}

type contextStmt struct {
	fakeDriverStmt
	checked   int
	converter driver.ValueConverter
}

func (s *contextStmt) ExecContext(context.Context, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}
func (s *contextStmt) QueryContext(context.Context, []driver.NamedValue) (driver.Rows, error) {
	return emptyRows{}, nil
}
func (s *contextStmt) CheckNamedValue(*driver.NamedValue) error  { s.checked++; return nil }
func (s *contextStmt) ColumnConverter(int) driver.ValueConverter { return s.converter }

// A driver with the context interfaces is traced through them: each statement
// records its own SQL and bind values under the context operation names, the
// transaction options reach the driver rather than being refused by the
// fallback, and Ping and the statement's argument checker are delegated.
func Test_sqlDriver_ContextInterfacesAreTraced(t *testing.T) {
	conn := &contextConn{stmt: &contextStmt{}}
	wrapped := WrapSQLDriver(contextDriverFull{conn}, DBInfo{DBType: ServiceTypeMysql, QueryType: ServiceTypeMysqlExecuteQuery})
	connector, err := wrapped.(driver.DriverContext).OpenConnector("dsn")
	require.NoError(t, err)
	db := sql.OpenDB(connector)
	defer db.Close()

	tracer := newTestAgent(defaultConfig()).NewSpanTracer("sql", "/sql")
	ctx := NewContext(context.Background(), tracer)

	require.NoError(t, db.PingContext(ctx))
	assert.True(t, conn.pinged, "Ping was not delegated to the driver")

	_, err = db.ExecContext(ctx, "UPDATE t SET a = ?", int64(1))
	require.NoError(t, err)
	rows, err := db.QueryContext(ctx, "SELECT ?", "x")
	require.NoError(t, err)
	require.NoError(t, rows.Close())

	stmt, err := db.PrepareContext(ctx, "INSERT INTO t VALUES (?)")
	require.NoError(t, err)
	assert.Equal(t, "INSERT INTO t VALUES (?)", conn.prepared)
	_, err = stmt.ExecContext(ctx, int64(2))
	require.NoError(t, err)
	rows, err = stmt.QueryContext(ctx, int64(3))
	require.NoError(t, err)
	require.NoError(t, rows.Close())
	require.NoError(t, stmt.Close())
	assert.Equal(t, 2, conn.stmt.checked, "the statement's argument checker was not consulted")

	// The fallback refuses both options; a ConnBeginTx driver must get them.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true})
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	assert.Equal(t, driver.IsolationLevel(sql.LevelSerializable), conn.txOpts.Isolation)
	assert.True(t, conn.txOpts.ReadOnly)

	var ops, binds []string
	for _, se := range tracer.(*span).spanEvents {
		ops = append(ops, se.operationName)
		assert.Equal(t, int32(ServiceTypeMysqlExecuteQuery), se.serviceType, se.operationName)
		if values := se.annotations.values; len(values) > 0 {
			binds = append(binds, values[0].s2)
		}
	}
	assert.Equal(t, []string{"ConnExecContext", "ConnQueryContext", "StmtExecContext", "StmtQueryContext", "BeginTx", "Commit"}, ops)
	assert.Equal(t, []string{"1", "x", "2", "3"}, binds, "bind values of the SQL events")
}

// A driver's own column converter is passed through, as database/sql would
// have used it on the unwrapped statement.
func Test_sqlStmt_ColumnConverterPassthrough(t *testing.T) {
	converter := driver.NotNull{Converter: driver.DefaultParameterConverter}
	stmt := &sqlStmt{Stmt: &contextStmt{converter: converter}, conn: newSqlConn(&fakeDriverConn{}, DBInfo{})}
	assert.Equal(t, converter, stmt.ColumnConverter(0))
}

type failingOpenDriver struct{ err error }

func (d failingOpenDriver) Open(string) (driver.Conn, error)               { return nil, d.err }
func (d failingOpenDriver) OpenConnector(string) (driver.Connector, error) { return nil, d.err }

// A driver that cannot open hands its own error back, with no connection or
// connector to wrap.
func TestWrapSQLDriver_OpenFailuresPassThrough(t *testing.T) {
	want := errors.New("bad dsn")
	wrapped := WrapSQLDriver(failingOpenDriver{want}, DBInfo{})

	conn, err := wrapped.Open("dsn")
	assert.ErrorIs(t, err, want)
	assert.Nil(t, conn)

	connector, err := wrapped.(driver.DriverContext).OpenConnector("dsn")
	assert.ErrorIs(t, err, want)
	assert.Nil(t, connector)
}
