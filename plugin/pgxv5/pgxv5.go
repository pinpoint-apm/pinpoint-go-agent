package pppgxv5

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

const serviceTypePgSqlExecuteQuery = 2501

type TracerPgx struct {
	span pinpoint.Tracer
}

// Checking interface implementations
var _ pgx.QueryTracer = (*TracerPgx)(nil)

func NewTracerPgx() *TracerPgx {
	return &TracerPgx{span: nil}
}

func (t *TracerPgx) TraceQueryStart(ctx context.Context, c *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	t.span = pinpoint.FromContext(ctx)
	ctx = pinpoint.NewContext(context.Background(), t.span)
	t.span.NewSpanEvent(SkippedFunctionName(2))
	stringArgs := composeArgs(data)

	se := t.span.SpanEvent()
	se.SetSQL(data.SQL, stringArgs)
	se.SetServiceType(serviceTypePgSqlExecuteQuery)
	se.SetEndPoint(c.Config().Host)
	se.SetDestination(c.Config().Database)
	se.Annotations().AppendString(pinpoint.AnnotationSqlId, data.SQL)
	return ctx
}

func (t *TracerPgx) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	t.span.EndSpanEvent()
}

// SkippedFunctionName returns function caller's name with skipped count.
func SkippedFunctionName(skip int) string {
	pc, _, _, ok := runtime.Caller(skip)
	if !ok {
		return ""
	}

	fn := runtime.FuncForPC(pc)
	result := fn.Name()

	cwd, err := os.Getwd()
	if err == nil {
		result = strings.TrimPrefix(result, cwd)
		moduleFnNames := strings.Split(result, "/")
		result = moduleFnNames[len(moduleFnNames)-1]
	}

	return result
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
