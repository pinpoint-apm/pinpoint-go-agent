package pinpoint

import (
	"cmp"
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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

// WrapSQLDriver wraps a driver.Driver and instruments SQL query calls.
// The returned driver implements driver.DriverContext exactly when drv does,
// so database/sql keeps taking the connector path for drivers that have it.
func WrapSQLDriver(drv driver.Driver, info DBInfo) driver.Driver {
	wrapped := &sqlDriver{Driver: drv, dbInfo: info}
	if _, ok := drv.(driver.DriverContext); ok {
		return wrappedSQLDriverContext{wrapped, wrapped}
	}
	return wrappedSQLDriver{wrapped}
}

// wrappedSQLDriver and wrappedSQLDriverContext are the two shapes WrapSQLDriver
// returns, named so that IsWrappedSQLDriver can tell them apart from a
// driver that has not been wrapped.
type (
	wrappedSQLDriver        struct{ driver.Driver }
	wrappedSQLDriverContext struct {
		driver.Driver
		driver.DriverContext
	}
)

// IsWrappedSQLDriver reports whether drv came from WrapSQLDriver. Code that
// wraps drivers as they are registered - the compile-time instrumentation
// tool's sql.Register hook - uses it to leave a driver a plugin already
// wrapped (mysql-pinpoint, pq-pinpoint, ...) alone, so that a query is
// recorded once.
func IsWrappedSQLDriver(drv driver.Driver) bool {
	switch drv.(type) {
	case *sqlDriver, wrappedSQLDriver, wrappedSQLDriverContext:
		return true
	}
	return false
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
	return sc.withSessionInterfaces(), nil
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
		return newSqlConn(conn, c.dbInfo).withSessionInterfaces(), nil
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

// driverConn is the connection the wrapper stands for, promoted to every shape
// withSessionInterfaces returns; UnwrapSQLConn reaches it through this method.
func (c *sqlConn) driverConn() driver.Conn {
	return c.Conn
}

// UnwrapSQLConn returns the driver's own connection under one that
// WrapSQLDriver wrapped, and conn itself for any other. database/sql's
// Conn.Raw hands its callback the connection the driver returned, which is the
// wrapper: a type assertion to the driver's own connection type, such as
// pgx's documented driverConn.(*stdlib.Conn), fails on it, and panics when
// unchecked. Assert on the unwrapped one instead:
//
//	err := conn.Raw(func(driverConn any) error {
//		pgxConn := pinpoint.UnwrapSQLConn(driverConn).(*stdlib.Conn).Conn()
//		...
//	})
//
// What runs on the unwrapped connection is not traced.
func UnwrapSQLConn(conn any) any {
	if w, ok := conn.(interface{ driverConn() driver.Conn }); ok {
		return w.driverConn()
	}
	return conn
}

// The shapes withSessionInterfaces returns: each adds to sqlConn exactly the
// session interfaces of the connection it wraps.
type (
	sqlConnResetter struct {
		*sqlConn
		driver.SessionResetter
	}
	sqlConnValidator struct {
		*sqlConn
		driver.Validator
	}
	sqlConnResetterValidator struct {
		*sqlConn
		driver.SessionResetter
		driver.Validator
	}
)

// withSessionInterfaces returns c with SessionResetter and Validator exactly
// when the wrapped connection has them. database/sql decides from their mere
// presence (beginDC's keepConnOnRollback) whether a connection survives a
// rollback its context triggered. Claiming both for a driver that has neither
// - go-ora v2, the sqlite drivers - put a connection whose rollback had just
// failed back in the pool with its transaction still open on the server,
// where go-ora's next statement committed it.
func (c *sqlConn) withSessionInterfaces() driver.Conn {
	r, isResetter := c.Conn.(driver.SessionResetter)
	v, isValidator := c.Conn.(driver.Validator)
	switch {
	case isResetter && isValidator:
		return sqlConnResetterValidator{c, r, v}
	case isResetter:
		return sqlConnResetter{c, r}
	case isValidator:
		return sqlConnValidator{c, v}
	}
	return c
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
// converting (NamedValueChecker) and Ping degrades to a no-op. Each method
// delegates when the underlying connection implements the interface and
// otherwise answers exactly as database/sql would have for a driver without
// it. SessionResetter and Validator cannot be answered that way, since their
// presence alone changes database/sql's behavior; withSessionInterfaces adds
// them only for a connection that has them.

func (c *sqlConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
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

// newSqlSpanEvent records one driver call as a span event: sql and its bind
// values args for a statement, both empty for a transaction boundary. The
// bind values are rendered only for a sampled tracer.
func (c *sqlConn) newSqlSpanEvent(ctx context.Context, operation string, start time.Time, err error, sql string, args []driver.NamedValue) {
	tracer := NewDatabaseTracer(ctx, operation, &c.dbInfo)
	defer tracer.EndSpanEvent()

	if tracer.IsSampled() {
		setSqlSpanEvent(tracer, start, err, sql, c.namedValueToString(args))
	}
}

func setSqlSpanEvent(tracer Tracer, start time.Time, err error, sql string, args string) {
	// One lookup: each SpanEvent() call takes the event stack lock.
	se := tracer.SpanEvent()
	se.SetSQL(sql, args)
	se.SetError(err, "SQL error")
	se.FixDuration(start, time.Now())
}

func (c *sqlConn) namedValueToString(named []driver.NamedValue) string {
	return bindValuesString(c.cfg(), len(named), func(i int) any { return named[i].Value })
}

// BindValuesString renders args as the bind value list of a SQL span event:
// each value abbreviated to SQL.MaxBindValueSize with a "...(n)" marker, and ""
// when SQL.TraceBindValue is off. It is what the database/sql wrapper records,
// for a plugin that instruments a driver outside database/sql (pgxv5) to hand
// to SetSQL as well.
func BindValuesString(args []any) string {
	return bindValuesString(GetConfig().load(), len(args), func(i int) any { return args[i] })
}

// bindValuesString renders n bind values, value(i) being the i-th, under cfg.
func bindValuesString(cfg *configSnapshot, n int, value func(int) any) string {
	if !cfg.sqlTraceBindValue || n == 0 {
		return ""
	}

	var b strings.Builder
	for i := 0; i < n; i++ {
		if !writeBindValue(&b, i, value(i), n-1, cfg.sqlMaxBindValueSize) {
			break
		}
	}
	return b.String()
}

func writeBindValue(b *strings.Builder, index int, value any, numComma int, maxSize int) bool {
	if maxSize <= 0 {
		return false
	}

	// The separator is written before the value that follows it, never after the
	// one before it, so it precedes whatever comes next: the next value, or the
	// count marker standing in for the values left out.
	if index > 0 {
		b.WriteString(", ")
	}
	if b.Len() >= maxSize {
		writeBindMarker(b, numComma+1)
		return false
	}
	writeAbbreviatedBindValue(b, value, maxSize)
	return true
}

// writeAbbreviatedBindValue writes one bind value abbreviated to maxSize.
//
// maxSize is the budget for the whole list, but it is spent per value: the value
// that finds any of it left writes up to maxSize of itself, so the list can
// reach roughly twice maxSize plus the markers. Cutting each value at what is
// left of the budget instead would bound the buffer more tightly but leave the
// last values as stubs, and it is a value's own head that a reader needs to
// recognize it by. maxBindValueAnnotationSize is what the span side reserves for
// the result.
func writeAbbreviatedBindValue(b *strings.Builder, value any, maxSize int) {
	if value, ok := value.(string); ok {
		writeAbbreviated(b, value, len(value), maxSize)
		return
	}

	// driver.Value is a closed set (int64, float64, bool, []byte, string,
	// time.Time, nil), so the common cases are formatted straight into the
	// buffer with no reflection and no intermediate string. Each branch writes
	// byte for byte what fmt.Sprint writes for the same value: %v formats a
	// float64 as strconv's shortest 'g', prints a time.Time through its String
	// method, and a nil interface as "<nil>". Anything a driver's
	// NamedValueChecker let through unconverted takes the fmt path below.
	var scratch [64]byte
	switch v := value.(type) {
	case nil:
		writeAbbreviated(b, "<nil>", len("<nil>"), maxSize)
		return
	case int64:
		writeAbbreviatedBytes(b, strconv.AppendInt(scratch[:0], v, 10), maxSize)
		return
	case float64:
		writeAbbreviatedBytes(b, strconv.AppendFloat(scratch[:0], v, 'g', -1, 64), maxSize)
		return
	case bool:
		writeAbbreviatedBytes(b, strconv.AppendBool(scratch[:0], v), maxSize)
		return
	case time.Time:
		s := v.String()
		writeAbbreviated(b, s, len(s), maxSize)
		return
	case []byte:
		writeAbbreviatedByteSlice(b, v, maxSize)
		return
	}

	// fmt.Sprint preserves the established "[1 2 3]" representation. Every
	// element adds at least one character to it, so no more than maxSize
	// elements can contribute to its prefix: slicing the rest away keeps a
	// million-element argument from being built whole to keep a kilobyte. The
	// length marker survives that slicing because an array reports its element
	// count, not the width of its rendering.
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Slice:
		elems := rv.Len()
		if elems > maxSize {
			value = rv.Slice(0, maxSize).Interface()
		}
		writeAbbreviated(b, fmt.Sprint(value), elems, maxSize)
		return
	case reflect.Map, reflect.Struct:
		if !rendersItself(value) {
			writeAbbreviatedComposite(b, rv, maxSize)
			return
		}
	}
	s := fmt.Sprint(value)
	writeAbbreviated(b, s, len(s), maxSize)
}

// rendersItself reports whether fmt.Sprint renders value through a method of
// its own rather than field by field.
func rendersItself(value any) bool {
	switch value.(type) {
	case fmt.Formatter, error, fmt.Stringer:
		return true
	}
	return false
}

// writeAbbreviatedComposite writes a map or a struct as fmt.Sprint renders it -
// "map[k:v ...]" with the keys in order, "{f ...}" - element by element, each
// abbreviated within what is left of maxSize, and cuts it once maxSize of it
// is written, the cut marked with its element count as a slice's is. Built
// whole by fmt.Sprint before the cut, a pgx.NamedArgs holding a 1 MiB document
// allocated megabytes for an annotation of a kilobyte.
func writeAbbreviatedComposite(b *strings.Builder, rv reflect.Value, maxSize int) {
	end := b.Len() + maxSize // where maxSize of the composite is written
	var keys []reflect.Value
	var n int
	if rv.Kind() == reflect.Map {
		keys = sortedMapKeys(rv)
		n = len(keys)
		b.WriteString("map[")
	} else {
		n = rv.NumField()
		b.WriteByte('{')
	}
	for i := range n {
		if i > 0 {
			b.WriteByte(' ')
		}
		if b.Len() >= end {
			writeBindMarker(b, n)
			return
		}
		if keys == nil {
			writeAbbreviatedElem(b, rv.Field(i), end-b.Len())
			continue
		}
		writeAbbreviatedElem(b, keys[i], end-b.Len())
		b.WriteByte(':')
		if b.Len() >= end {
			writeBindMarker(b, n)
			return
		}
		writeAbbreviatedElem(b, rv.MapIndex(keys[i]), end-b.Len())
	}
	if keys == nil {
		b.WriteByte('}')
	} else {
		b.WriteByte(']')
	}
}

// writeAbbreviatedElem writes one element of a composite. An unexported struct
// field cannot be taken out as an interface value, so fmt renders it whole.
func writeAbbreviatedElem(b *strings.Builder, v reflect.Value, maxSize int) {
	if v.CanInterface() {
		writeAbbreviatedBindValue(b, v.Interface(), maxSize)
		return
	}
	s := fmt.Sprint(v)
	writeAbbreviated(b, s, len(s), maxSize)
}

// sortedMapKeys returns the keys of a map in the order fmt prints them for the
// usual key kinds: numbers by value, strings and anything else by rendering.
func sortedMapKeys(rv reflect.Value) []reflect.Value {
	keys := rv.MapKeys()
	slices.SortFunc(keys, func(a, b reflect.Value) int {
		switch a.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return cmp.Compare(a.Int(), b.Int())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			return cmp.Compare(a.Uint(), b.Uint())
		case reflect.Float32, reflect.Float64:
			return cmp.Compare(a.Float(), b.Float())
		case reflect.String:
			return strings.Compare(a.String(), b.String())
		}
		return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
	})
	return keys
}

// writeAbbreviatedByteSlice writes v as fmt.Sprint does ("[1 2 3]") without
// building a string of the whole slice: elements are formatted into a scratch
// buffer only until it holds more than maxSize bytes, since anything past that
// is cut anyway. The cut is marked with the number of bytes in the slice, which
// is the fact a reader wants; counting the characters of its decimal rendering
// would mean walking every element the cut exists to avoid formatting.
func writeAbbreviatedByteSlice(b *strings.Builder, v []byte, maxSize int) {
	var scratch [128]byte
	buf := append(scratch[:0], '[')
	for i, e := range v {
		if len(buf) > maxSize {
			break
		}
		if i > 0 {
			buf = append(buf, ' ')
		}
		buf = strconv.AppendUint(buf, uint64(e), 10)
	}
	buf = append(buf, ']')
	// The loop stops only past maxSize, so a buf within it is the whole
	// rendering and needs no marker at all.
	if len(buf) <= maxSize {
		b.Write(buf)
		return
	}
	b.Write(buf[:maxSize])
	writeBindMarker(b, len(v))
}

// writeAbbreviatedBytes is writeAbbreviated for a value formatted into a
// scratch buffer: the buffer holds the whole value, and every byte of it is
// ASCII, so the cut needs no rune boundary.
func writeAbbreviatedBytes(b *strings.Builder, value []byte, maxSize int) {
	if len(value) <= maxSize {
		b.Write(value)
		return
	}
	b.Write(value[:maxSize])
	writeBindMarker(b, len(value))
}

// writeAbbreviated writes value cut to maxSize, marking the cut with valueLen -
// the length of the value itself, which is not always the length of the text
// being cut: an array reports how many elements it holds. The cut lands on a
// rune boundary: protobuf rejects invalid UTF-8 string fields at marshal time,
// so a mid-rune cut would fail the whole span carrying the annotation.
func writeAbbreviated(b *strings.Builder, value string, valueLen int, maxSize int) {
	if len(value) <= maxSize {
		b.WriteString(value)
		return
	}
	cut := maxSize
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	b.WriteString(value[:cut])
	writeBindMarker(b, valueLen)
}

// writeBindMarker appends the "...(n)" marker that reports one of two events,
// both of which can appear in one list: after a value cut with the last of
// the budget, n is that value's length (writeAbbreviated*); after a list ended
// early, n is how many values the statement had (writeBindValue), and the
// count marker follows the cut value on the next round. Either way n is the
// size a reader cannot otherwise recover, since the limit is already known.
//
// The marker lands past the limit rather than cutting back over what is
// written: making room inside a limit shorter than the marker would drop the
// marker itself and leave the truncation with no trace at all.
func writeBindMarker(b *strings.Builder, n int) {
	b.WriteString("...(" + strconv.Itoa(n) + ")")
}

func (c *sqlConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	start := time.Now()

	if ec, ok := c.Conn.(driver.ExecerContext); ok {
		result, err := ec.ExecContext(ctx, query, args)

		if err != driver.ErrSkip {
			c.newSqlSpanEvent(ctx, "ConnExecContext", start, err, query, args)
		}

		return result, err
	}

	e, ok := c.Conn.(driver.Execer) //lint:ignore SA1019 fallback for a driver without ExecerContext
	if !ok {
		return nil, driver.ErrSkip
	}

	dargs, err := legacyArgs(ctx, args)
	if err != nil {
		return nil, err
	}

	result, err := e.Exec(query, dargs)
	if err != driver.ErrSkip {
		c.newSqlSpanEvent(ctx, "ConnExec", start, err, query, args)
	}

	return result, err
}

func (c *sqlConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	start := time.Now()

	if qc, ok := c.Conn.(driver.QueryerContext); ok {
		rows, err := qc.QueryContext(ctx, query, args)
		if err != driver.ErrSkip {
			c.newSqlSpanEvent(ctx, "ConnQueryContext", start, err, query, args)
		}

		return rows, err
	}

	q, ok := c.Conn.(driver.Queryer) //lint:ignore SA1019 fallback for a driver without QueryerContext
	if !ok {
		return nil, driver.ErrSkip
	}

	dargs, err := legacyArgs(ctx, args)
	if err != nil {
		return nil, err
	}

	rows, err := q.Query(query, dargs)
	if err != driver.ErrSkip {
		c.newSqlSpanEvent(ctx, "ConnQuery", start, err, query, args)
	}

	return rows, err
}

func (c *sqlConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	var tx driver.Tx
	var err error

	start := time.Now()
	if cbt, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err = cbt.BeginTx(ctx, opts)
		if cfg := c.cfg(); cfg.sqlTraceCommit || cfg.sqlTraceRollback {
			c.newSqlSpanEvent(ctx, "BeginTx", start, err, "", nil)
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

	tx, err = c.Conn.Begin() //lint:ignore SA1019 fallback for a driver without ConnBeginTx
	if err == nil {
		select {
		case <-ctx.Done():
			tx.Rollback()
			return nil, ctx.Err()
		default:
		}
	}
	if cfg := c.cfg(); cfg.sqlTraceCommit || cfg.sqlTraceRollback {
		c.newSqlSpanEvent(ctx, "Begin", start, err, "", nil)
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
		t.conn.newSqlSpanEvent(t.ctx, "Commit", start, err, "", nil)
	}
	return err
}

func (t *sqlTx) Rollback() (err error) {
	start := time.Now()
	err = t.Tx.Rollback()
	if t.conn.cfg().sqlTraceRollback {
		t.conn.newSqlSpanEvent(t.ctx, "Rollback", start, err, "", nil)
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
	//lint:ignore SA1019 passed through for a driver that still implements it
	if cc, ok := s.Stmt.(driver.ColumnConverter); ok {
		return cc.ColumnConverter(idx)
	}
	return driver.DefaultParameterConverter
}

func (s *sqlStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	start := time.Now()

	if sec, ok := s.Stmt.(driver.StmtExecContext); ok {
		result, err := sec.ExecContext(ctx, args)
		s.conn.newSqlSpanEvent(ctx, "StmtExecContext", start, err, s.sql, args)
		return result, err
	}

	dargs, err := legacyArgs(ctx, args)
	if err != nil {
		return nil, err
	}

	result, err := s.Stmt.Exec(dargs) //lint:ignore SA1019 fallback for a statement without StmtExecContext
	s.conn.newSqlSpanEvent(ctx, "StmtExec", start, err, s.sql, args)
	return result, err
}

func (s *sqlStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	start := time.Now()

	if sqc, ok := s.Stmt.(driver.StmtQueryContext); ok {
		rows, err := sqc.QueryContext(ctx, args)
		s.conn.newSqlSpanEvent(ctx, "StmtQueryContext", start, err, s.sql, args)
		return rows, err
	}

	dargs, err := legacyArgs(ctx, args)
	if err != nil {
		return nil, err
	}

	rows, err := s.Stmt.Query(dargs) //lint:ignore SA1019 fallback for a statement without StmtQueryContext
	s.conn.newSqlSpanEvent(ctx, "StmtQuery", start, err, s.sql, args)
	return rows, err
}

// legacyArgs converts the arguments for a driver without the Context
// interfaces, as database/sql/ctxutil.go does: a named parameter is refused,
// and a context already done fails the call before it reaches the driver.
func legacyArgs(ctx context.Context, args []driver.NamedValue) ([]driver.Value, error) {
	dargs, err := namedValueToValue(args)
	if err != nil {
		return nil, err
	}
	select {
	default:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return dargs, nil
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
