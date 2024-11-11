package pppgxv5

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

type pgxTracer struct{}

var (
	// Checking interface implementations
	_ pgx.QueryTracer    = (*pgxTracer)(nil)
	_ pgx.BatchTracer    = (*pgxTracer)(nil)
	_ pgx.ConnectTracer  = (*pgxTracer)(nil)
	_ pgx.CopyFromTracer = (*pgxTracer)(nil)

	onceLoadConfig    sync.Once
	gTraceBindValue   bool
	gMaxBindValueSize int
)

func initTracerOptions(opt []string, initFunc func()) {
	initFunc()
	pinpoint.GetConfig().AddReloadCallback(opt, initFunc)
}

// NewTracer creates a tracer to instrument jackc/pgx calls.
func NewTracer() *pgxTracer {
	onceLoadConfig.Do(func() {
		initTracerOptions(
			[]string{
				pinpoint.CfgSQLTraceBindValue,
				pinpoint.CfgSQLMaxBindValueSize,
			},
			func() {
				cfg := pinpoint.GetConfig()
				gTraceBindValue = cfg.Bool(pinpoint.CfgSQLTraceBindValue)
				gMaxBindValueSize = cfg.Int(pinpoint.CfgSQLMaxBindValueSize)
			},
		)
	})

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
	if tracer := newSpanEvent(ctx, c.Config(), "pgx.Query"); tracer.IsSampled() {
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
	newSpanEvent(ctx, c.Config(), "pgx.Batch")
	return ctx
}

func (t *pgxTracer) TraceBatchQuery(ctx context.Context, c *pgx.Conn, data pgx.TraceBatchQueryData) {
	if tracer := newSpanEvent(ctx, c.Config(), "pgx.BatchQuery"); tracer.IsSampled() {
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
	if tracer := newSpanEvent(ctx, c.Config(), "pgx.CopyFrom"); tracer.IsSampled() {
		se := tracer.SpanEvent()
		se.Annotations().AppendString(pinpoint.AnnotationArgs0, data.TableName.Sanitize())
	}

	return ctx
}

func (t *pgxTracer) TraceCopyFromEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromEndData) {
	if tracer := pinpoint.FromContext(ctx); tracer.IsSampled() {
		defer tracer.EndSpanEvent()

		se := tracer.SpanEvent()
		se.SetError(data.Err, "pgx.CopyFrom error")
	}
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
	if args == nil || len(args) == 0 || !gTraceBindValue {
		return ""
	}

	var b bytes.Buffer
	numComma := len(args) - 1

	for i, v := range args {
		if !writeArg(&b, i, v, numComma, gMaxBindValueSize) {
			break
		}
	}

	return b.String()
}

func writeArg(b *bytes.Buffer, index int, value any, numComma int, maxSize int) bool {
	b.WriteString(fmt.Sprint(value))
	if index < numComma {
		b.WriteString(", ")
	}
	if b.Len() > maxSize {
		b.WriteString("...(")
		b.WriteString(fmt.Sprint(maxSize))
		b.WriteString(")")
		return false
	}
	return true
}
