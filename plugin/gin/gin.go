package gin

import (
	"bytes"
	"github.com/gin-gonic/gin"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !pinpoint.GetAgent().Enable() {
			c.Next()
			return
		}

		tracer := phttp.NewHttpServerTracer(c.Request, "Gin Server")
		defer tracer.EndSpan()

		if !tracer.IsSampled() {
			c.Next()
			return
		}

		tracer.NewSpanEvent(handlerFuncName(c.HandlerName()))
		defer tracer.EndSpanEvent()

		c.Request = pinpoint.RequestWithTracerContext(c.Request, tracer)
		c.Next()
		if len(c.Errors) > 0 {
			tracer.Span().SetError(c.Errors.Last())
		}

		phttp.RecordHttpServerResponse(tracer, c.Writer.Status(), c.Writer.Header())
	}
}

func handlerFuncName(funcName string) string {
	var buf bytes.Buffer
	buf.WriteString(funcName)
	buf.WriteString("(*gin.Context)")
	return buf.String()
}
