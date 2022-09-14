package gin

import (
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

		c.Request = pinpoint.RequestWithTracerContext(c.Request, tracer)
		defer tracer.NewSpanEvent(c.HandlerName()).EndSpanEvent()

		c.Next()
		if len(c.Errors) > 0 {
			tracer.Span().SetError(c.Errors.Last())
		}

		phttp.RecordHttpServerResponse(tracer, c.Writer.Status(), c.Writer.Header())
	}
}
