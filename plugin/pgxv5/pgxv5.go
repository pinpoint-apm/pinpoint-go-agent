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

func (t *TracerPgx) TraceQueryStart(ctx context.Context, c *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return ctx
	}
	tracer.NewSpanEvent("pgx")
	sqlArgs := composeArgs(data)

	ctx = pinpoint.NewContext(ctx, tracer)
	ctx = context.WithValue(ctx, "sql", data.SQL)
	ctx = context.WithValue(ctx, "sqlargs", sqlArgs)
	return ctx
}

func (t *TracerPgx) TraceQueryEnd(ctx context.Context, c *pgx.Conn, data pgx.TraceQueryEndData) {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return
	}
	defer tracer.EndSpanEvent()

	sql := ctx.Value("sql")
	if sql == nil {
		return
	}

	sqlArgs := ctx.Value("sqlargs")
	if sqlArgs == nil {
		return
	}

	se := tracer.SpanEvent()
	se.SetSQL(sql.(string), sqlArgs.(string))
	se.SetServiceType(serviceTypePgSqlExecuteQuery)
	se.SetEndPoint(c.Config().Host)
	se.SetDestination(c.Config().Database)
	se.Annotations().AppendString(pinpoint.AnnotationSqlId, sql.(string))
	se.SetError(data.Err)
}

func composeArgs(data pgx.TraceQueryStartData) string {
	stringArgs := ""
	for _, v := range data.Args {
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
