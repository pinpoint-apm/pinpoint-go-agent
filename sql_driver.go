package pinpoint

import (
	"context"
	"database/sql/driver"
	"errors"
	"time"
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

func makeDriver(drv *sqlDriver) driver.Driver {
	if _, ok := drv.Driver.(driver.DriverContext); ok {
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
	return makeDriver(&sqlDriver{trace: dt, Driver: d})
}

type sqlDriver struct {
	driver.Driver
	trace DatabaseTrace
}

func (d *sqlDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.Driver.Open(name)
	if err != nil {
		return nil, err
	}

	psc := &sqlConn{
		Conn:  conn,
		trace: d.trace,
	}

	parseDSN(&psc.trace, name)
	return psc, nil
}

func (d *sqlDriver) OpenConnector(name string) (driver.Connector, error) {
	conn, err := d.Driver.(driver.DriverContext).OpenConnector(name)
	if err != nil {
		return nil, err
	}

	psc := &sqlConnector{
		Connector: conn,
		trace:     d.trace,
	}

	parseDSN(&psc.trace, name)
	return psc, nil
}

type sqlConnector struct {
	driver.Connector
	trace DatabaseTrace
}

func (c *sqlConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if nil != err {
		return nil, err
	}
	return &sqlConn{
		Conn:  conn,
		trace: c.trace,
	}, nil
}

func (c *sqlConnector) Driver() driver.Driver {
	return makeDriver(&sqlDriver{
		Driver: c.Connector.Driver(),
		trace:  c.trace,
	})
}

type sqlConn struct {
	driver.Conn
	trace DatabaseTrace
}

func prepare(stmt driver.Stmt, err error, td *DatabaseTrace, query string) (driver.Stmt, error) {
	if nil != err {
		return nil, err
	}

	td.QueryString = query
	return &sqlStmt{
		Stmt:  stmt,
		trace: td,
	}, nil
}

func (c *sqlConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(query)
	return prepare(stmt, err, &c.trace, query)
}

func (c *sqlConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if cpc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		stmt, err := cpc.PrepareContext(ctx, query)
		return prepare(stmt, err, &c.trace, query)
	}

	return c.Prepare(query)
}

func newSqlSpanEvent(ctx context.Context, operation string, dt *DatabaseTrace, start time.Time, err error) {
	if tracer := NewDatabaseTracer(ctx, operation, dt); tracer != nil {
		tracer.SpanEvent().SetError(err)
		tracer.SpanEvent().FixDuration(start, time.Now())
		tracer.EndSpanEvent()
	}
}

func (c *sqlConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	start := time.Now()

	if ec, ok := c.Conn.(driver.ExecerContext); ok {
		result, err := ec.ExecContext(ctx, query, args)

		if err != driver.ErrSkip {
			c.trace.QueryString = query
			newSqlSpanEvent(ctx, "ConnExecContext", &c.trace, start, err)
		}

		return result, err
	}

	// sourced: database/sql/cxtutil.go
	dargs, err := namedValueToValue(args)
	if err != nil {
		return nil, err
	}
	select {
	default:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if e, ok := c.Conn.(driver.Execer); ok {
		result, err := e.Exec(query, dargs)
		if err != driver.ErrSkip {
			c.trace.QueryString = query
			newSqlSpanEvent(ctx, "ConnExec", &c.trace, start, err)
		}

		return result, err
	}

	return nil, driver.ErrSkip
}

func (c *sqlConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	start := time.Now()

	if qc, ok := c.Conn.(driver.QueryerContext); ok {
		rows, err := qc.QueryContext(ctx, query, args)
		if err != driver.ErrSkip {
			c.trace.QueryString = query
			newSqlSpanEvent(ctx, "ConnQueryContext", &c.trace, start, err)
		}

		return rows, err
	}

	// sourced: database/sql/cxtutil.go
	dargs, err := namedValueToValue(args)
	if err != nil {
		return nil, err
	}
	select {
	default:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if q, ok := c.Conn.(driver.Queryer); ok {
		rows, err := q.Query(query, dargs)
		if err != driver.ErrSkip {
			c.trace.QueryString = query
			newSqlSpanEvent(ctx, "ConnQuery", &c.trace, start, err)
		}

		return rows, err
	}

	return nil, driver.ErrSkip
}

type sqlStmt struct {
	driver.Stmt
	trace *DatabaseTrace
}

func (s *sqlStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	start := time.Now()

	if sec, ok := s.Stmt.(driver.StmtExecContext); ok {
		result, err := sec.ExecContext(ctx, args)
		newSqlSpanEvent(ctx, "StmtExecContext", s.trace, start, err)
		return result, err
	}

	// sourced: database/sql/cxtutil.go
	dargs, err := namedValueToValue(args)
	if err != nil {
		return nil, err
	}
	select {
	default:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	result, err := s.Stmt.Exec(dargs)
	newSqlSpanEvent(ctx, "StmtExec", s.trace, start, err)
	return result, err
}

func (s *sqlStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	start := time.Now()

	if sqc, ok := s.Stmt.(driver.StmtQueryContext); ok {
		rows, err := sqc.QueryContext(ctx, args)
		newSqlSpanEvent(ctx, "StmtQueryContext", s.trace, start, err)
		return rows, err
	}

	// sourced: database/sql/cxtutil.go
	dargs, err := namedValueToValue(args)
	if err != nil {
		return nil, err
	}
	select {
	default:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	rows, err := s.Stmt.Query(dargs)
	newSqlSpanEvent(ctx, "StmtQuery", s.trace, start, err)
	return rows, err
}

// sourced: database/sql/cxtutil.go
func namedValueToValue(named []driver.NamedValue) ([]driver.Value, error) {
	dargs := make([]driver.Value, len(named))
	for n, param := range named {
		if len(param.Name) > 0 {
			return nil, errors.New("sql: driver does not support the use of Named Parameters")
		}
		dargs[n] = param.Value
	}
	return dargs, nil
}
