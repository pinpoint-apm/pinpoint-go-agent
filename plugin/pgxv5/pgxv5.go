package pppgxv5

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strconv"
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
	recordCopyFromTarget(newSpanEvent(ctx, c.Config(), "pgx.CopyFrom"), data.TableName)
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

	// The separator is written before the value that follows it, never after
	// the one before it, so it precedes whatever comes next: the next value,
	// or the count marker standing in for the values left out. This mirrors
	// the agent's own driver wrapper, which follows Java's
	// BindValueUtils.bindValueToString.
	if index > 0 {
		b.WriteString(", ")
	}
	if b.Len() >= maxSize {
		writeArgCountMarker(b, numComma+1)
		return false
	}
	writeAbbreviatedArg(b, value, maxSize)
	return true
}

// writeAbbreviatedArg writes one argument abbreviated to maxSize.
//
// maxSize is the budget for the whole list, but it is spent per value: the
// value that finds any of it left writes up to maxSize of itself, so the list
// can reach roughly twice maxSize plus the markers. It is the value's own
// head, not the list's total, that a reader needs to recognize which argument
// this was, and the agent reserves the same room for the result.
func writeAbbreviatedArg(b *bytes.Buffer, value any, maxSize int) {
	if value, ok := value.(string); ok {
		writeAbbreviated(b, value, len(value), maxSize)
		return
	}

	// fmt.Sprint preserves the established "[1 2 3]" representation. Every
	// element adds at least one character to it, so no more than maxSize
	// elements can contribute to its prefix: slicing the rest away keeps a
	// million-element array parameter from being built whole to keep a
	// kilobyte. The length marker survives that slicing because an array
	// reports its element count, not the width of its rendering - as Java's
	// ArrayUtils.abbreviate reports a byte[] bind value.
	if rv := reflect.ValueOf(value); rv.Kind() == reflect.Slice {
		elems := rv.Len()
		if elems > maxSize {
			value = rv.Slice(0, maxSize).Interface()
		}
		writeAbbreviated(b, fmt.Sprint(value), elems, maxSize)
		return
	}
	s := fmt.Sprint(value)
	writeAbbreviated(b, s, len(s), maxSize)
}

// writeAbbreviated writes value cut to maxSize, marking the cut with valueLen -
// the length of the value itself, which is not always the length of the text
// being cut: an array reports how many elements it holds. The cut lands on a
// rune boundary: protobuf rejects invalid UTF-8 string fields at marshal time,
// so a mid-rune cut would fail the whole span carrying the annotation.
func writeAbbreviated(b *bytes.Buffer, value string, valueLen int, maxSize int) {
	if len(value) <= maxSize {
		b.WriteString(value)
		return
	}
	cut := maxSize
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	b.WriteString(value[:cut])
	writeArgLengthMarker(b, valueLen)
}

// The two markers report two different events, and both can appear in one
// list: a value abbreviated with the last of the budget is followed by the
// count marker on the next round. writeArgLengthMarker says one value was cut
// and how long it was; writeArgCountMarker says the list itself ended early
// and how many arguments the statement had. Both land past the limit rather
// than cutting back over what is written: making room inside a limit shorter
// than the marker would drop the marker itself and leave the truncation with
// no trace at all.
func writeArgLengthMarker(b *bytes.Buffer, valueLen int) {
	b.WriteString("...(" + strconv.Itoa(valueLen) + ")")
}

func writeArgCountMarker(b *bytes.Buffer, numValues int) {
	b.WriteString("...(" + strconv.Itoa(numValues) + ")")
}
