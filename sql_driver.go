package pinpoint

import (
	"context"
	"database/sql/driver"
)

type DatabaseTrace struct {
	DbType      int
	QueryType   int
	DbName      string
	DbHost      string
	QueryString string

	ParseDSN func(td *DatabaseTrace, dataSourceName string)
}

func parseDSN(dt *DatabaseTrace, dsn string) {
	if f := dt.ParseDSN; nil != f {
		f(dt, dsn)
	}
}

func NewDatabaseTracer(ctx context.Context, funcName string, dt *DatabaseTrace) Tracer {
	tracer := FromContext(ctx)
	if tracer == nil {
		return nil
	}

	tracer.NewSpanEvent(funcName)
	se := tracer.SpanEvent()
	se.SetServiceType(int32(dt.QueryType))
	se.SetEndPoint(dt.DbHost)
	se.SetDestination(dt.DbName)
	se.SetSQL(dt.QueryString)

	return tracer
}

func NewDatabaseTracerWithQuery(ctx context.Context, funcName string, dt *DatabaseTrace, sql string) Tracer {
	dt.QueryString = sql
	return NewDatabaseTracer(ctx, funcName, dt)
}

func makeDriver(drv *PinpointSqlDriver) driver.Driver {
	if _, ok := drv.originDriver.(driver.DriverContext); ok {
		return struct {
			driver.Driver
			driver.DriverContext
		}{drv, drv}
	} else {
		return struct {
			driver.Driver
		}{drv}
	}
}

func MakePinpointSQLDriver(d driver.Driver, dt DatabaseTrace) driver.Driver {
	return makeDriver(&PinpointSqlDriver{trace: dt, originDriver: d})
}

type PinpointSqlDriver struct {
	trace        DatabaseTrace
	originDriver driver.Driver
}

func (d *PinpointSqlDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.originDriver.Open(name)
	if err != nil {
		return nil, err
	}

	psc := &PinpointSqlConn{
		originConn: conn,
		trace:      d.trace,
	}

	parseDSN(&psc.trace, name)
	return psc, nil
}

func (d *PinpointSqlDriver) OpenConnector(name string) (driver.Connector, error) {
	conn, err := d.originDriver.(driver.DriverContext).OpenConnector(name)
	if err != nil {
		return nil, err
	}

	psc := &PinpointSqlConnector{
		originConnector: conn,
		trace:           d.trace,
	}

	parseDSN(&psc.trace, name)
	return psc, nil
}

type PinpointSqlConnector struct {
	trace           DatabaseTrace
	originConnector driver.Connector
}

func (c *PinpointSqlConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.originConnector.Connect(ctx)
	if nil != err {
		return nil, err
	}
	return &PinpointSqlConn{
		trace:      c.trace,
		originConn: conn,
	}, nil
}

func (c *PinpointSqlConnector) Driver() driver.Driver {
	return makeDriver(&PinpointSqlDriver{
		trace:        c.trace,
		originDriver: c.originConnector.Driver(),
	})
}

type PinpointSqlConn struct {
	trace      DatabaseTrace
	originConn driver.Conn
}

func prepare(stmt driver.Stmt, err error, td *DatabaseTrace, query string) (driver.Stmt, error) {
	if nil != err {
		return nil, err
	}

	td.QueryString = query
	return &PinpointSqlStmt{
		trace:      td,
		originStmt: stmt,
	}, nil
}

func (c *PinpointSqlConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.originConn.Prepare(query)
	return prepare(stmt, err, &c.trace, query)
}

func (c *PinpointSqlConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	stmt, err := c.originConn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
	return prepare(stmt, err, &c.trace, query)
}

func (c *PinpointSqlConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	tracer := NewDatabaseTracerWithQuery(ctx, "ExecContext", &c.trace, query)
	result, err := c.originConn.(driver.ExecerContext).ExecContext(ctx, query, args)
	if tracer != nil {
		tracer.EndSpanEvent()
	}
	return result, err
}

func (c *PinpointSqlConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	tracer := NewDatabaseTracerWithQuery(ctx, "QueryContext", &c.trace, query)
	rows, err := c.originConn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if tracer != nil {
		tracer.EndSpanEvent()
	}
	return rows, err
}

func (c *PinpointSqlConn) Begin() (driver.Tx, error) {
	return c.originConn.Begin()
}

func (c *PinpointSqlConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.originConn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (c *PinpointSqlConn) CheckNamedValue(v *driver.NamedValue) error {
	return c.originConn.(driver.NamedValueChecker).CheckNamedValue(v)
}

func (c *PinpointSqlConn) Close() error {
	return c.originConn.Close()
}

func (c *PinpointSqlConn) Exec(query string, args []driver.Value) (driver.Result, error) {
	return c.originConn.(driver.Execer).Exec(query, args)
}

func (c *PinpointSqlConn) Ping(ctx context.Context) error {
	return c.originConn.(driver.Pinger).Ping(ctx)
}

func (c *PinpointSqlConn) Query(query string, args []driver.Value) (driver.Rows, error) {
	return c.originConn.(driver.Queryer).Query(query, args)
}

func (c *PinpointSqlConn) ResetSession(ctx context.Context) error {
	if _, ok := c.originConn.(driver.SessionResetter); ok {
		return c.originConn.(driver.SessionResetter).ResetSession(ctx)
	}
	return nil
}

type PinpointSqlStmt struct {
	trace      *DatabaseTrace
	originStmt driver.Stmt
}

func (s *PinpointSqlStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	tracer := NewDatabaseTracer(ctx, "StmtExecContext", s.trace)
	result, err := s.originStmt.(driver.StmtExecContext).ExecContext(ctx, args)
	if tracer != nil {
		tracer.EndSpanEvent()
	}
	return result, err
}

func (s *PinpointSqlStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	tracer := NewDatabaseTracer(ctx, "StmtQueryContext", s.trace)
	rows, err := s.originStmt.(driver.StmtQueryContext).QueryContext(ctx, args)
	if tracer != nil {
		tracer.EndSpanEvent()
	}
	return rows, err
}

func (s *PinpointSqlStmt) CheckNamedValue(v *driver.NamedValue) error {
	return s.originStmt.(driver.NamedValueChecker).CheckNamedValue(v)
}

func (s *PinpointSqlStmt) Close() error {
	return s.originStmt.Close()
}

func (s *PinpointSqlStmt) ColumnConverter(idx int) driver.ValueConverter {
	return s.originStmt.(driver.ColumnConverter).ColumnConverter(idx)
}

func (s *PinpointSqlStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.originStmt.Exec(args)
}

func (s *PinpointSqlStmt) NumInput() int {
	return s.originStmt.NumInput()
}

func (s *PinpointSqlStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.originStmt.Query(args)
}
