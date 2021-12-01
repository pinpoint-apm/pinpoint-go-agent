package gin

import (
	"github.com/gin-gonic/gin"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func Middleware(agent pinpoint.Agent) gin.HandlerFunc {
	apiId := agent.RegisterSpanApiId("Go Gin Server", pinpoint.ApiTypeWebRequest)

	return func(c *gin.Context) {
		if agent.Enable() {
			tracer := phttp.NewHttpServerTracer(agent, c.Request, "Gin Server")
			defer tracer.EndSpan()
			tracer.Span().SetApiId(apiId)

			c.Request = pinpoint.RequestWithTracerContext(c.Request, tracer)
			defer tracer.NewSpanEvent(c.HandlerName()).EndSpanEvent()

			c.Next()

			phttp.TraceHttpStatus(tracer, c.Writer.Status())

			if len(c.Errors) > 0 {
				tracer.Span().SetError(c.Errors.Last())
			}
		} else {
			c.Next()
		}
	}
}
