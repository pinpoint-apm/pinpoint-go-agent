package pppgxv5

import (
	"context"
	"runtime"
	"sync"
	"time"
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

// connectKey carries the goroutine tracer a connect is recorded on from
// TraceConnectStart to TraceConnectEnd.
type connectKey struct{}

// TraceConnectStart records the connect on a goroutine tracer of its own.
// pgxpool connects on puddle's constructor goroutine with the values of the
// Acquire ctx, so the request's tracer was written from a second goroutine,
// and an Acquire canceled mid-connect let the late TraceConnectEnd end
// whichever event the request had open by then. A connect made with no event
// open is not recorded: a goroutine tracer has nothing to link to.
func (t *pgxTracer) TraceConnectStart(ctx context.Context, c pgx.TraceConnectStartData) context.Context {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return ctx
	}
	fork := tracer.NewGoroutineTracer()
	ctx = context.WithValue(pinpoint.NewContext(ctx, fork), connectKey{}, fork)
	newSpanEvent(ctx, c.ConnConfig, "pgx.Connect")
	return ctx
}

func (t *pgxTracer) TraceConnectEnd(ctx context.Context, data pgx.TraceConnectEndData) {
	if tracer, ok := ctx.Value(connectKey{}).(pinpoint.Tracer); ok && tracer.IsSampled() {
		defer tracer.EndSpan()
		defer tracer.EndSpanEvent()
		// Guarded at every site: SetError is an interface method, so its
		// variadic name escapes and allocated on every call, nil error and all.
		if data.Err != nil {
			tracer.SpanEvent().SetError(data.Err, "pgx.Connect error")
		}
	}
}

// TraceQueryStart opens the event TraceQueryEnd closes, so the work done per
// row of an open Rows nests under its query. That needs every start to get its
// end: pgx v5.10.0's Exec and Query returned without one when deallocating
// invalidated statements failed, which is why the module requires v5.11.0.
func (t *pgxTracer) TraceQueryStart(ctx context.Context, c *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if tracer := connSpanEvent(ctx, c, "pgx.Query"); tracer.IsSampled() {
		se := tracer.SpanEvent()
		sqlArgs := pinpoint.BindValuesString(data.Args)
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

// batchEndKey carries a batch's ended flag from TraceBatchStart to
// TraceBatchEnd. pgx calls TraceBatchEnd twice for a batch that fails before
// it is sent - from SendBatch, and again from the BatchResults.Close the
// caller must make - and the second EndSpanEvent ended the caller's own event,
// recording the batch error on it.
type batchEndKey struct{}

func (t *pgxTracer) TraceBatchStart(ctx context.Context, c *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	if tracer := connSpanEvent(ctx, c, "pgx.Batch"); tracer.IsSampled() {
		return batchStarted(ctx)
	}
	return ctx
}

// batchStarted is the context TraceBatchStart hands on past the event it
// opened. A plain bool: SendBatch's call returns before the caller can Close.
func batchStarted(ctx context.Context) context.Context {
	return context.WithValue(ctx, batchEndKey{}, new(bool))
}

func (t *pgxTracer) TraceBatchQuery(ctx context.Context, c *pgx.Conn, data pgx.TraceBatchQueryData) {
	if tracer := connSpanEvent(ctx, c, "pgx.BatchQuery"); tracer.IsSampled() {
		defer tracer.EndSpanEvent()

		se := tracer.SpanEvent()
		sqlArgs := pinpoint.BindValuesString(data.Args)
		se.SetSQL(data.SQL, sqlArgs)
		if data.Err != nil {
			se.SetError(data.Err, "pgx.BatchQuery error")
		}
	}
}

func (t *pgxTracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	if ended, ok := ctx.Value(batchEndKey{}).(*bool); ok && !*ended {
		*ended = true
		tracer := pinpoint.FromContext(ctx)
		defer tracer.EndSpanEvent()
		if data.Err != nil {
			tracer.SpanEvent().SetError(data.Err, "pgx.Batch error")
		}
	}
}

// copyFromStart is what TraceCopyFromStart hands TraceCopyFromEnd, which
// records the event whole. CopyFrom returns without calling TraceCopyFromEnd
// when it cannot describe the target - a table or column that does not exist,
// a permission it lacks - and an event opened at the start stayed open, with
// every later event of the request nested under it.
type copyFromStart struct {
	at     time.Time
	target pgx.Identifier
}

type copyFromKey struct{}

func (t *pgxTracer) TraceCopyFromStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromStartData) context.Context {
	if !pinpoint.FromContext(ctx).IsSampled() {
		return ctx
	}
	return context.WithValue(ctx, copyFromKey{}, &copyFromStart{at: time.Now(), target: data.TableName})
}

func (t *pgxTracer) TraceCopyFromEnd(ctx context.Context, c *pgx.Conn, data pgx.TraceCopyFromEndData) {
	if start, ok := ctx.Value(copyFromKey{}).(*copyFromStart); ok {
		recordCopyFrom(connSpanEvent(ctx, c, "pgx.CopyFrom"), start, data.Err)
	}
}

// recordCopyFrom completes and ends the event connSpanEvent opened for a
// finished CopyFrom: its start time, and the copy target, which CopyFrom
// records in place of a SQL statement. Separate from the callback because pgx
// hands that a live *pgx.Conn, which no test can build; this is what a test
// can reach.
func recordCopyFrom(tracer pinpoint.Tracer, start *copyFromStart, err error) {
	defer tracer.EndSpanEvent()
	se := tracer.SpanEvent()
	se.FixDuration(start.at, time.Now())
	se.Annotations().AppendString(pinpoint.AnnotationArgs0, start.target.Sanitize())
	if err != nil {
		se.SetError(err, "pgx.CopyFrom error")
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
