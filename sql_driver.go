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

	ParseDSN func(info *DBInfo, dsn string)
}

func parseDSN(info *DBInfo, dsn string) {
	if f := info.ParseDSN; f != nil {
		f(info, dsn)
	}
}

// NewDatabaseTracer returns a Tracer for database operation.
func NewDatabaseTracer(ctx context.Context, funcName string, info *DBInfo) Tracer {
	tracer := FromContext(ctx)
	tracer.NewSpanEvent(funcName)
	se := tracer.SpanEvent()
	se.SetServiceType(int32(info.QueryType))
	se.SetEndPoint(info.DBHost)
	se.SetDestination(info.DBName)

	return tracer
}

func wrapDriver(drv *sqlDriver) driver.Driver {
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

// WrapSQLDriver wraps a driver.Driver and instruments SQL query calls.
func WrapSQLDriver(drv driver.Driver, info DBInfo) driver.Driver {
	return wrapDriver(&sqlDriver{Driver: drv, dbInfo: info})
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

	sc := newSqlConn(conn, d.dbInfo)
	parseDSN(&sc.dbInfo, name)
	return sc, nil
}

func (d *sqlDriver) OpenConnector(name string) (driver.Connector, error) {
	conn, err := d.Driver.(driver.DriverContext).OpenConnector(name)
	if err != nil {
		return nil, err
	}

	sc := &sqlConnector{
		Connector: conn,
		dbInfo:    d.dbInfo,
		driver:    d,
	}

	parseDSN(&sc.dbInfo, name)
	return sc, nil
}

type sqlConnector struct {
	driver.Connector
	dbInfo DBInfo
	driver *sqlDriver
}

func (c *sqlConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if conn, err := c.Connector.Connect(ctx); err != nil {
		return nil, err
	} else {
		return newSqlConn(conn, c.dbInfo), nil
	}
}

func (c *sqlConnector) Driver() driver.Driver {
	return c.driver
}

type sqlConn struct {
	driver.Conn
	dbInfo DBInfo
}

func newSqlConn(conn driver.Conn, dbInfo DBInfo) *sqlConn {
	return &sqlConn{
		Conn:   conn,
		dbInfo: dbInfo,
	}
}

// cfg resolves the settings per operation rather than pinning the Config the
// connection was opened with. A connection opened before NewAgent - a Ping
// from package init, a pool warmed at startup - captured the noop agent's own
// Config, and no setting of the real agent, nor any reload, ever reached that
// long-lived pooled connection.
func (c *sqlConn) cfg() *configSnapshot {
	return GetConfig().load()
}

// The wrapper embeds the driver.Conn interface, which hides the underlying
// connection's optional interfaces from database/sql's type assertions:
// without the passthroughs below, a driver's custom argument types stop
// converting (NamedValueChecker), dead connections get reused after network
// blips (Validator/SessionResetter) and Ping degrades to a no-op. Each method
// delegates when the underlying connection implements the interface and
// otherwise answers exactly as database/sql would have for a driver without
// it.

func (c *sqlConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *sqlConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *sqlConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *sqlConn) CheckNamedValue(nv *driver.NamedValue) error {
	if nvc, ok := c.Conn.(driver.NamedValueChecker); ok {
		return nvc.CheckNamedValue(nv)
	}
	return driver.ErrSkip // use the default argument handling
}

func prepare(stmt driver.Stmt, err error, conn *sqlConn, sql string) (driver.Stmt, error) {
	if nil != err {
		return nil, err
	}

	return &sqlStmt{
		Stmt: stmt,
		conn: conn,
		sql:  sql,
	}, nil
}

func (c *sqlConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if cpc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		stmt, err := cpc.PrepareContext(ctx, query)
		return prepare(stmt, err, c, query)
	}

	// database/sql routes every Prepare here because the wrapper implements
	// driver.ConnPrepareContext, so its own fallback check never runs; mirror
	// it (database/sql/ctxutil.go). Without it a canceled or expired context
	// yields a live statement instead of an error, and that statement stays
	// open on the connection with no owner to close it.
	stmt, err := c.Conn.Prepare(query)
	if err == nil {
		select {
		default:
		case <-ctx.Done():
			stmt.Close()
			return nil, ctx.Err()
		}
	}
	return prepare(stmt, err, c, query)
}

func (c *sqlConn) newSqlSpanEventWithNamedValue(ctx context.Context, operation string, start time.Time, err error, sql string, args []driver.NamedValue) {
	tracer := NewDatabaseTracer(ctx, operation, &c.dbInfo)
	defer tracer.EndSpanEvent()

	if tracer.IsSampled() {
		setSqlSpanEvent(tracer, start, err, sql, c.namedValueToString(args))
	}
}

func (c *sqlConn) newSqlSpanEventWithValue(ctx context.Context, operation string, start time.Time, err error, sql string, args []driver.Value) {
	tracer := NewDatabaseTracer(ctx, operation, &c.dbInfo)
	defer tracer.EndSpanEvent()

	if tracer.IsSampled() {
		setSqlSpanEvent(tracer, start, err, sql, c.valueToString(args))
	}
}

func (c *sqlConn) newSqlSpanEventNoSql(ctx context.Context, operation string, start time.Time, err error) {
	tracer := NewDatabaseTracer(ctx, operation, &c.dbInfo)
	defer tracer.EndSpanEvent()

	if tracer.IsSampled() {
		setSqlSpanEvent(tracer, start, err, "", "")
	}
}

func setSqlSpanEvent(tracer Tracer, start time.Time, err error, sql string, args string) {
	tracer.SpanEvent().SetSQL(sql, args)
	tracer.SpanEvent().SetError(err, "SQL error")
	tracer.SpanEvent().FixDuration(start, time.Now())
}

func (c *sqlConn) namedValueToString(named []driver.NamedValue) string {
	cfg := c.cfg()
	if !cfg.sqlTraceBindValue || named == nil {
		return ""
	}

	var b bytes.Buffer
	numComma := len(named) - 1
	for i, param := range named {
		if !writeBindValue(&b, i, param.Value, numComma, cfg.sqlMaxBindValueSize) {
			break
		}
	}
	return b.String()
}

func (c *sqlConn) valueToString(values []driver.Value) string {
	cfg := c.cfg()
	if !cfg.sqlTraceBindValue || values == nil {
		return ""
	}

	var b bytes.Buffer
	numComma := len(values) - 1
	for i, v := range values {
		if !writeBindValue(&b, i, v, numComma, cfg.sqlMaxBindValueSize) {
			break
		}
	}
	return b.String()
}

func writeBindValue(b *bytes.Buffer, index int, value interface{}, numComma int, maxSize int) bool {
	remaining := maxSize + 1 - b.Len()
	if remaining > 0 {
		switch v := value.(type) {
		case string:
			writeBindString(b, v, remaining)
		case []byte:
			// Each byte contributes at least one output byte, so formatting only
			// this prefix is enough to reproduce the retained fmt.Sprint prefix.
			if len(v) > remaining {
				v = v[:remaining]
			}
			writeBindString(b, fmt.Sprint(v), remaining)
		default:
			writeBindString(b, fmt.Sprint(value), remaining)
		}
	}
	if index < numComma && b.Len() <= maxSize {
		writeBindString(b, ", ", maxSize+1-b.Len())
	}
	if b.Len() > maxSize {
		b.Truncate(maxSize)
		b.WriteString("...(")
		b.WriteString(fmt.Sprint(maxSize))
		b.WriteString(")")
		return false
	}
	return true
}

func writeBindString(b *bytes.Buffer, value string, limit int) {
	if len(value) > limit {
		value = value[:limit]
	}
	b.WriteString(value)
}

func (c *sqlConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	start := time.Now()

	if ec, ok := c.Conn.(driver.ExecerContext); ok {
		result, err := ec.ExecContext(ctx, query, args)

		if err != driver.ErrSkip {
			c.newSqlSpanEventWithNamedValue(ctx, "ConnExecContext", start, err, query, args)
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
			c.newSqlSpanEventWithValue(ctx, "ConnExec", start, err, query, dargs)
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
			c.newSqlSpanEventWithNamedValue(ctx, "ConnQueryContext", start, err, query, args)
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
			c.newSqlSpanEventWithValue(ctx, "ConnQuery", start, err, query, dargs)
		}

		return rows, err
	}

	return nil, driver.ErrSkip
}

func (c *sqlConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	var tx driver.Tx
	var err error

	start := time.Now()
	if cbt, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err = cbt.BeginTx(ctx, opts)
		if cfg := c.cfg(); cfg.sqlTraceCommit || cfg.sqlTraceRollback {
			c.newSqlSpanEventNoSql(ctx, "BeginTx", start, err)
			if err == nil {
				tx = &sqlTx{tx, c, ctx}
			}
		}
		return tx, err
	}

	// database/sql routes every BeginTx here because the wrapper implements
	// driver.ConnBeginTx, so its own fallback checks never run; mirror them
	// (database/sql/ctxutil.go). Silently calling Begin would downgrade a
	// Serializable or read-only request to a default read-write transaction.
	if opts.Isolation != 0 { // 0 == sql.LevelDefault
		return nil, errors.New("sql: driver does not support non-default isolation level")
	}
	if opts.ReadOnly {
		return nil, errors.New("sql: driver does not support read-only transactions")
	}

	tx, err = c.Conn.Begin()
	if err == nil {
		select {
		case <-ctx.Done():
			tx.Rollback()
			return nil, ctx.Err()
		default:
		}
	}
	if cfg := c.cfg(); cfg.sqlTraceCommit || cfg.sqlTraceRollback {
		c.newSqlSpanEventNoSql(ctx, "Begin", start, err)
		if err == nil {
			tx = &sqlTx{tx, c, ctx}
		}
	}
	return tx, err
}

type sqlTx struct {
	driver.Tx
	conn *sqlConn
	ctx  context.Context
}

func (t *sqlTx) Commit() (err error) {
	start := time.Now()
	err = t.Tx.Commit()
	if t.conn.cfg().sqlTraceCommit {
		t.conn.newSqlSpanEventNoSql(t.ctx, "Commit", start, err)
	}
	return err
}

func (t *sqlTx) Rollback() (err error) {
	start := time.Now()
	err = t.Tx.Rollback()
	if t.conn.cfg().sqlTraceRollback {
		t.conn.newSqlSpanEventNoSql(t.ctx, "Rollback", start, err)
	}
	return err
}

type sqlStmt struct {
	driver.Stmt
	conn *sqlConn
	sql  string
}

// CheckNamedValue mirrors database/sql's own checker selection for the wrapped
// pair: it consults only the outermost statement's checker, so this must fall
// back from the underlying statement to the underlying connection before
// yielding to the default handling.
func (s *sqlStmt) CheckNamedValue(nv *driver.NamedValue) error {
	if nvc, ok := s.Stmt.(driver.NamedValueChecker); ok {
		return nvc.CheckNamedValue(nv)
	}
	if nvc, ok := s.conn.Conn.(driver.NamedValueChecker); ok {
		return nvc.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

func (s *sqlStmt) ColumnConverter(idx int) driver.ValueConverter {
	if cc, ok := s.Stmt.(driver.ColumnConverter); ok {
		return cc.ColumnConverter(idx)
	}
	return driver.DefaultParameterConverter
}

func (s *sqlStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	start := time.Now()

	if sec, ok := s.Stmt.(driver.StmtExecContext); ok {
		result, err := sec.ExecContext(ctx, args)
		s.conn.newSqlSpanEventWithNamedValue(ctx, "StmtExecContext", start, err, s.sql, args)
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
	s.conn.newSqlSpanEventWithValue(ctx, "StmtExec", start, err, s.sql, dargs)
	return result, err
}

func (s *sqlStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	start := time.Now()

	if sqc, ok := s.Stmt.(driver.StmtQueryContext); ok {
		rows, err := sqc.QueryContext(ctx, args)
		s.conn.newSqlSpanEventWithNamedValue(ctx, "StmtQueryContext", start, err, s.sql, args)
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
	s.conn.newSqlSpanEventWithValue(ctx, "StmtQuery", start, err, s.sql, dargs)
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
