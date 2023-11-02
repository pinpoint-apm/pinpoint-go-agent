package pppgxv5

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// Checking interface implementations
var _ pgx.QueryTracer = (*TracerPgx)(nil)

const serviceTypePgSqlExecuteQuery = 2501

type TracerPgx struct {
}

func NewTracerPgx() *TracerPgx {
	return &TracerPgx{}
}

func (t *TracerPgx) TraceConnectStart(ctx context.Context, c pgx.TraceConnectStartData) context.Context {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return ctx
	}

	config := c.ConnConfig
	se := tracer.NewSpanEvent("pgx.Connect").SpanEvent()
	se.SetServiceType(serviceTypePgSqlExecuteQuery)
	se.SetEndPoint(config.Host)
	se.SetDestination(config.Database)

	return ctx
}

func (t *TracerPgx) TraceConnectEnd(ctx context.Context, data pgx.TraceConnectEndData) {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return
	}

	tracer.EndSpanEvent()
}

func (t *TracerPgx) TraceQueryStart(ctx context.Context, c *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return ctx
	}

	config := c.Config()
	se := tracer.NewSpanEvent("pgx.Query").SpanEvent()
	se.SetServiceType(serviceTypePgSqlExecuteQuery)
	se.SetEndPoint(config.Host)
	se.SetDestination(config.Database)

	sqlArgs := composeArgs(data.Args)
	se.SetSQL(data.SQL, sqlArgs)

	return ctx
}

func (t *TracerPgx) TraceQueryEnd(ctx context.Context, c *pgx.Conn, data pgx.TraceQueryEndData) {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return
	}

	defer tracer.EndSpanEvent()

	se := tracer.SpanEvent()
	se.SetError(data.Err, "SQL error")
}

func (t *TracerPgx) TraceBatchStart(ctx context.Context, c *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return ctx
	}

	config := c.Config()
	se := tracer.NewSpanEvent("pgx.Batch").SpanEvent()
	se.SetServiceType(serviceTypePgSqlExecuteQuery)
	se.SetEndPoint(config.Host)
	se.SetDestination(config.Database)

	return ctx
}

func (t *TracerPgx) TraceBatchQuery(ctx context.Context, c *pgx.Conn, data pgx.TraceBatchQueryData) {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return
	}

	config := c.Config()
	se := tracer.NewSpanEvent("pgx.BatchQuery").SpanEvent()
	defer tracer.EndSpanEvent()

	se.SetServiceType(serviceTypePgSqlExecuteQuery)
	se.SetEndPoint(config.Host)
	se.SetDestination(config.Database)

	sqlArgs := composeArgs(data.Args)
	se.SetSQL(data.SQL, sqlArgs)
	se.SetError(data.Err, "SQL error")
}

func (t *TracerPgx) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return
	}

	defer tracer.EndSpanEvent()

	se := tracer.SpanEvent()
	se.SetError(data.Err, "Batch error")
}

func (t *TracerPgx) TraceCopyFromStart(ctx context.Context, c *pgx.Conn, data pgx.TraceCopyFromStartData) context.Context {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return ctx
	}

	config := c.Config()
	se := tracer.NewSpanEvent("pgx.CopyFrom").SpanEvent()
	se.SetServiceType(serviceTypePgSqlExecuteQuery)
	se.SetEndPoint(config.Host)
	se.SetDestination(config.Database)
	se.Annotations().AppendString(pinpoint.AnnotationArgs0, data.TableName.Sanitize())

	return ctx
}

func (t *TracerPgx) TraceCopyFromEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromEndData) {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return
	}

	defer tracer.EndSpanEvent()

	se := tracer.SpanEvent()
	se.SetError(data.Err, "CopyFrom error")
}

func composeArgs(args []any) string {
	stringArgs := ""
	for _, v := range args {
		arg := ""
		switch val := v.(type) {
		case pgtype.Timestamp:
			if val.Valid {
				arg = fmt.Sprintf("%v", val.Time.Format(time.RFC3339))
			} else {
				arg = "<nil>"
			}
		case pgtype.Date:
			if val.Valid {
				arg = fmt.Sprintf("%v", val.Time.Format(time.RFC3339))
			} else {
				arg = "<nil>"
			}
		case pgtype.Int4:
			if val.Valid {
				arg = fmt.Sprintf("%v", val.Int32)
			} else {
				arg = "<nil>"
			}
		case pgtype.Float8:
			if val.Valid {
				arg = fmt.Sprintf("%v", val.Float64)
			} else {
				arg = "<nil>"
			}
		case pgtype.Text:
			if val.Valid {
				arg = fmt.Sprintf("%v", val.String)
			} else {
				arg = "<nil>"
			}
		case pgtype.Bool:
			if val.Valid {
				arg = fmt.Sprintf("%t", val.Bool)
			} else {
				arg = "<nil>"
			}
		default:
			if val != nil {
				arg = fmt.Sprintf("%v", val)
			} else {
				arg = "<nil>"
			}
		}
		stringArgs += fmt.Sprintf("%v,", arg)
	}

	return stringArgs
}
