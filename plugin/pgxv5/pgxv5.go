package pppgxv5

import (
	"context"
	"runtime"
	"sync"
	"weak"

	"github.com/jackc/pgx/v5"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

type pgxTracer struct{}

// connInfos caches the DBInfo of each live connection. pgx's Conn.Config() is
// not an accessor but a deep copy of the ConnConfig - a tls.Config clone under
// its lock, the runtime parameter map, the fallbacks: 5 allocations and ~1KB
// per call - and it was paid on every sampled query and every statement of a
// batch for two strings that never change for a connection. Weak keys, since
// pgx has no close hook and a plain map would pin every pooled connection ever
// opened; the cleanup drops the entry when the connection is collected.
var connInfos sync.Map // weak.Pointer[pgx.Conn] -> *pinpoint.DBInfo

func connDBInfo(c *pgx.Conn) *pinpoint.DBInfo {
	k := weak.Make(c)
	if v, ok := connInfos.Load(k); ok {
		return v.(*pinpoint.DBInfo)
	}
	config := c.Config()
	info := &pinpoint.DBInfo{QueryType: pinpoint.ServiceTypePgSqlExecuteQuery, DBName: config.Database, DBHost: config.Host}
	if v, loaded := connInfos.LoadOrStore(k, info); loaded {
		return v.(*pinpoint.DBInfo)
	}
	runtime.AddCleanup(c, func(k weak.Pointer[pgx.Conn]) { connInfos.Delete(k) }, k)
	return info
}

var (
	// Checking interface implementations
	_ pgx.QueryTracer    = (*pgxTracer)(nil)
	_ pgx.BatchTracer    = (*pgxTracer)(nil)
	_ pgx.ConnectTracer  = (*pgxTracer)(nil)
	_ pgx.CopyFromTracer = (*pgxTracer)(nil)
)

// NewTracer creates a tracer to instrument jackc/pgx calls.
func NewTracer() *pgxTracer {
	return &pgxTracer{}
}

func (t *pgxTracer) TraceConnectStart(ctx context.Context, c pgx.TraceConnectStartData) context.Context {
	newSpanEvent(ctx, c.ConnConfig, "pgx.Connect")
	return ctx
}

func (t *pgxTracer) TraceConnectEnd(ctx context.Context, data pgx.TraceConnectEndData) {
	if tracer := pinpoint.FromContext(ctx); tracer.IsSampled() {
		defer tracer.EndSpanEvent()
		// Guarded at every site: SetError is an interface method, so its
		// variadic name escapes and allocated on every call, nil error and all.
		if data.Err != nil {
			tracer.SpanEvent().SetError(data.Err, "pgx.Connect error")
		}
	}
}

func (t *pgxTracer) TraceQueryStart(ctx context.Context, c *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if tracer := connSpanEvent(ctx, c, "pgx.Query"); tracer.IsSampled() {
		se := tracer.SpanEvent()
		sqlArgs := t.composeArgs(data.Args)
		se.SetSQL(data.SQL, sqlArgs)
	}

	return ctx
}

func (t *pgxTracer) TraceQueryEnd(ctx context.Context, c *pgx.Conn, data pgx.TraceQueryEndData) {
	if tracer := pinpoint.FromContext(ctx); tracer.IsSampled() {
		defer tracer.EndSpanEvent()
		if data.Err != nil {
			tracer.SpanEvent().SetError(data.Err, "pgx.Query error")
		}
	}
}

func (t *pgxTracer) TraceBatchStart(ctx context.Context, c *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	connSpanEvent(ctx, c, "pgx.Batch")
	return ctx
}

func (t *pgxTracer) TraceBatchQuery(ctx context.Context, c *pgx.Conn, data pgx.TraceBatchQueryData) {
	if tracer := connSpanEvent(ctx, c, "pgx.BatchQuery"); tracer.IsSampled() {
		defer tracer.EndSpanEvent()

		se := tracer.SpanEvent()
		sqlArgs := t.composeArgs(data.Args)
		se.SetSQL(data.SQL, sqlArgs)
		if data.Err != nil {
			se.SetError(data.Err, "pgx.BatchQuery error")
		}
	}
}

func (t *pgxTracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	if tracer := pinpoint.FromContext(ctx); tracer.IsSampled() {
		defer tracer.EndSpanEvent()
		if data.Err != nil {
			tracer.SpanEvent().SetError(data.Err, "pgx.Batch error")
		}
	}
}

func (t *pgxTracer) TraceCopyFromStart(ctx context.Context, c *pgx.Conn, data pgx.TraceCopyFromStartData) context.Context {
	recordCopyFromTarget(connSpanEvent(ctx, c, "pgx.CopyFrom"), data.TableName)
	return ctx
}

// recordCopyFromTarget annotates the copy target, which CopyFrom records in
// place of a SQL statement. Separate from the callback because pgx hands that
// a live *pgx.Conn, which no test can build; this is what a test can reach.
func recordCopyFromTarget(tracer pinpoint.Tracer, target pgx.Identifier) {
	if tracer.IsSampled() {
		tracer.SpanEvent().Annotations().AppendString(pinpoint.AnnotationArgs0, target.Sanitize())
	}
}

func (t *pgxTracer) TraceCopyFromEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromEndData) {
	if tracer := pinpoint.FromContext(ctx); tracer.IsSampled() {
		defer tracer.EndSpanEvent()
		if data.Err != nil {
			tracer.SpanEvent().SetError(data.Err, "pgx.CopyFrom error")
		}
	}
}

// connSpanEvent is newSpanEvent for a live connection, reading the DBInfo off
// connInfos. The sampling check comes first so an unsampled query touches
// neither the cache nor, on a miss, Conn.Config().
func connSpanEvent(ctx context.Context, c *pgx.Conn, cmd string) pinpoint.Tracer {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return tracer
	}
	return pinpoint.NewDatabaseTracer(ctx, cmd, connDBInfo(c))
}

func newSpanEvent(ctx context.Context, config *pgx.ConnConfig, cmd string) pinpoint.Tracer {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return tracer
	}
	info := pinpoint.DBInfo{QueryType: pinpoint.ServiceTypePgSqlExecuteQuery, DBName: config.Database, DBHost: config.Host}
	return pinpoint.NewDatabaseTracer(ctx, cmd, &info)
}

func (t *pgxTracer) composeArgs(args []any) string {
	return pinpoint.BindValuesString(args)
}
