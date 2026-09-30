package pppgxv5

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

type pgxTracer struct{}

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
		tracer.EndSpanEvent()
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

		se := tracer.SpanEvent()
		se.SetError(data.Err, "pgx.Query error")
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
		se.SetError(data.Err, "pgx.BatchQuery error")
	}
}

func (t *pgxTracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	if tracer := pinpoint.FromContext(ctx); tracer.IsSampled() {
		defer tracer.EndSpanEvent()

		se := tracer.SpanEvent()
		se.SetError(data.Err, "pgx.Batch error")
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

		se := tracer.SpanEvent()
		se.SetError(data.Err, "pgx.CopyFrom error")
	}
}

// connSpanEvent is newSpanEvent for a live connection. The sampling check
// comes first: pgx's Conn.Config() is not an accessor but a deep copy of the
// ConnConfig - a tls.Config clone, the runtime parameter map, the fallbacks -
// which an unsampled query paid for on every call and threw away.
func connSpanEvent(ctx context.Context, c *pgx.Conn, cmd string) pinpoint.Tracer {
	if tracer := pinpoint.FromContext(ctx); !tracer.IsSampled() {
		return tracer
	}
	return newSpanEvent(ctx, c.Config(), cmd)
}

func newSpanEvent(ctx context.Context, config *pgx.ConnConfig, cmd string) pinpoint.Tracer {
	tracer := pinpoint.FromContext(ctx)
	if tracer.IsSampled() {
		se := tracer.NewSpanEvent(cmd).SpanEvent()
		se.SetServiceType(pinpoint.ServiceTypePgSqlExecuteQuery)
		se.SetEndPoint(config.Host)
		se.SetDestination(config.Database)
	}

	return tracer
}

func (t *pgxTracer) composeArgs(args []any) string {
	return pinpoint.BindValuesString(args)
}
