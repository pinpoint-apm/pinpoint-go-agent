package pinpoint

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"
)

type DBInfo struct {
	DBType    int
	QueryType int
	DBName    string
	DBHost    string

	ParseDSN func(info *DBInfo, dataSourceName string)
}

func parseDSN(info *DBInfo, dsn string) {
	if f := info.ParseDSN; f != nil {
		f(info, dsn)
	}
}

func NewDatabaseTracer(ctx context.Context, funcName string, info *DBInfo) Tracer {
	tracer := FromContext(ctx)
	if tracer == nil {
		return nil
	}

	tracer.NewSpanEvent(funcName)
	se := tracer.SpanEvent()
	se.SetServiceType(int32(info.QueryType))
	se.SetEndPoint(info.DBHost)
	se.SetDestination(info.DBName)

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

func MakePinpointSQLDriver(d driver.Driver, info DBInfo) driver.Driver {
	return makeDriver(&sqlDriver{Driver: d, dbInfo: info})
}

type sqlDriver struct {
	driver.Driver
	dbInfo DBInfo
}

func (d *sqlDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.Driver.Open(name)
	if err != nil {
		return nil, err
	}

	psc := &sqlConn{
		Conn:   conn,
		dbInfo: &d.dbInfo,
	}

	parseDSN(psc.dbInfo, name)
	return psc, nil
}

func (d *sqlDriver) OpenConnector(name string) (driver.Connector, error) {
	conn, err := d.Driver.(driver.DriverContext).OpenConnector(name)
	if err != nil {
		return nil, err
	}

	psc := &sqlConnector{
		Connector: conn,
		dbInfo:    &d.dbInfo,
	}

	parseDSN(psc.dbInfo, name)
	return psc, nil
}

type sqlConnector struct {
	driver.Connector
	dbInfo *DBInfo
}

func (c *sqlConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if nil != err {
		return nil, err
	}
	return &sqlConn{
		Conn:   conn,
		dbInfo: c.dbInfo,
	}, nil
}

func (c *sqlConnector) Driver() driver.Driver {
	return makeDriver(&sqlDriver{
		Driver: c.Connector.Driver(),
		dbInfo: *c.dbInfo,
	})
}

type sqlConn struct {
	driver.Conn
	dbInfo *DBInfo
}

func prepare(stmt driver.Stmt, err error, info *DBInfo, query string) (driver.Stmt, error) {
	if nil != err {
		return nil, err
	}

	return &sqlStmt{
		Stmt:   stmt,
		dbInfo: info,
		sql:    query,
	}, nil
}

func (c *sqlConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if cpc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		stmt, err := cpc.PrepareContext(ctx, query)
		return prepare(stmt, err, c.dbInfo, query)
	}

	stmt, err := c.Conn.Prepare(query)
	return prepare(stmt, err, c.dbInfo, query)
}

func newSqlSpanEvent(ctx context.Context, operation string, info *DBInfo, sql string, args string, start time.Time, err error) {
	if tracer := NewDatabaseTracer(ctx, operation, info); tracer != nil {
		tracer.SpanEvent().SetSQL(sql, args)
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
			newSqlSpanEvent(ctx, "ConnExecContext", c.dbInfo, query, namedValueToString(args), start, err)
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
			newSqlSpanEvent(ctx, "ConnExec", c.dbInfo, query, valueToString(dargs), start, err)
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
			newSqlSpanEvent(ctx, "ConnQueryContext", c.dbInfo, query, namedValueToString(args), start, err)
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
			newSqlSpanEvent(ctx, "ConnQuery", c.dbInfo, query, valueToString(dargs), start, err)
		}

		return rows, err
	}

	return nil, driver.ErrSkip
}

func (c *sqlConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	start := time.Now()

	if cbt, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err := cbt.BeginTx(ctx, opts)
		newSqlSpanEvent(ctx, "BeginTx", c.dbInfo, "", "", start, err)
		if err != nil {
			return nil, err
		}
		return &sqlTx{tx, c.dbInfo, ctx}, nil
	}

	tx, err := c.Conn.Begin()
	newSqlSpanEvent(ctx, "Begin", c.dbInfo, "", "", start, err)
	if err != nil {
		return nil, err
	}
	return &sqlTx{tx, c.dbInfo, ctx}, nil
}

type sqlTx struct {
	driver.Tx
	dbInfo *DBInfo
	ctx    context.Context
}

func (t *sqlTx) Commit() (err error) {
	start := time.Now()
	err = t.Tx.Commit()
	newSqlSpanEvent(t.ctx, "Commit", t.dbInfo, "", "", start, err)
	return err
}

func (t *sqlTx) Rollback() (err error) {
	start := time.Now()
	err = t.Tx.Rollback()
	newSqlSpanEvent(t.ctx, "Rollback", t.dbInfo, "", "", start, err)
	return err
}

type sqlStmt struct {
	driver.Stmt
	dbInfo *DBInfo
	sql    string
}

func (s *sqlStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	start := time.Now()

	if sec, ok := s.Stmt.(driver.StmtExecContext); ok {
		result, err := sec.ExecContext(ctx, args)
		newSqlSpanEvent(ctx, "StmtExecContext", s.dbInfo, s.sql, namedValueToString(args), start, err)
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
	newSqlSpanEvent(ctx, "StmtExec", s.dbInfo, s.sql, valueToString(dargs), start, err)
	return result, err
}

func (s *sqlStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	start := time.Now()

	if sqc, ok := s.Stmt.(driver.StmtQueryContext); ok {
		rows, err := sqc.QueryContext(ctx, args)
		newSqlSpanEvent(ctx, "StmtQueryContext", s.dbInfo, s.sql, namedValueToString(args), start, err)
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
	newSqlSpanEvent(ctx, "StmtQuery", s.dbInfo, s.sql, valueToString(dargs), start, err)
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

const maxBindArgsLength = 1024

func namedValueToString(named []driver.NamedValue) string {
	var b bytes.Buffer

	c := len(named) - 1
	for i, param := range named {
		b.WriteString(fmt.Sprint(param.Value))
		if i < c {
			b.WriteString(", ")
		}
		if b.Len() > maxBindArgsLength {
			b.WriteString("...(1024)")
			break
		}
	}

	return b.String()
}

func valueToString(values []driver.Value) string {
	var b bytes.Buffer

	c := len(values) - 1
	for i, v := range values {
		b.WriteString(fmt.Sprint(v))
		if i < c {
			b.WriteString(", ")
		}
		if b.Len() > maxBindArgsLength {
			b.WriteString("...(1024)")
			break
		}
	}

	return b.String()
}
