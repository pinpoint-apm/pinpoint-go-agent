package pppgxv5

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"unicode/utf8"

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
	cfg := pinpoint.GetConfig()
	if len(args) == 0 || !cfg.Bool(pinpoint.CfgSQLTraceBindValue) {
		return ""
	}

	var b bytes.Buffer
	numComma := len(args) - 1
	maxSize := cfg.Int(pinpoint.CfgSQLMaxBindValueSize)

	for i, v := range args {
		if !writeArg(&b, i, v, numComma, maxSize) {
			break
		}
	}

	return b.String()
}

func writeArg(b *bytes.Buffer, index int, value any, numComma int, maxSize int) bool {
	if maxSize <= 0 {
		return false
	}

	complete := writeLimitedArgValue(b, value, maxSize)
	if complete && index < numComma {
		complete = writeLimitedString(b, ", ", maxSize)
	}
	if !complete {
		writeArgTruncationMarker(b, maxSize)
	}
	return complete
}

func writeLimitedArgValue(b *bytes.Buffer, value any, maxSize int) bool {
	if value, ok := value.(string); ok {
		return writeLimitedString(b, value, maxSize)
	}

	remaining := maxSize - b.Len()
	if remaining <= 0 {
		return false
	}
	// fmt.Sprint preserves the established "[1 2 3]" representation. Every
	// element adds at least one character to it, so no more than remaining
	// elements can contribute to its prefix.
	if rv := reflect.ValueOf(value); rv.Kind() == reflect.Slice && rv.Len() > remaining {
		value = rv.Slice(0, remaining).Interface()
	}
	return writeLimitedString(b, fmt.Sprint(value), maxSize)
}

func writeLimitedString(b *bytes.Buffer, value string, maxSize int) bool {
	if len(value) == 0 {
		return b.Len() <= maxSize
	}

	remaining := maxSize - b.Len()
	if len(value) <= remaining {
		b.WriteString(value)
		return true
	}
	if remaining <= 0 {
		return false
	}

	for remaining > 0 && !utf8.RuneStart(value[remaining]) {
		remaining--
	}
	b.WriteString(value[:remaining])
	return false
}

func writeArgTruncationMarker(b *bytes.Buffer, maxSize int) {
	marker := "...(" + fmt.Sprint(maxSize) + ")"
	if len(marker) > maxSize {
		truncateArg(b, maxSize)
		return
	}

	truncateArg(b, maxSize-len(marker))
	b.WriteString(marker)
}

func truncateArg(b *bytes.Buffer, maxSize int) {
	if maxSize >= b.Len() {
		return
	}
	for maxSize > 0 && !utf8.RuneStart(b.Bytes()[maxSize]) {
		maxSize--
	}
	b.Truncate(maxSize)
}
