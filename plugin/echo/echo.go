package echo

import (
	"github.com/labstack/echo"

	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func Middleware(agent *pinpoint.Agent) echo.MiddlewareFunc {
	if agent == nil {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}

	apiId := agent.RegisterSpanApiId("Go Echo Server", pinpoint.ApiTypeWebRequest)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request()

			tracer := phttp.NewHttpServerTracer(agent, req, "Echo Server")
			defer tracer.EndSpan()
			tracer.Span().SetApiId(apiId)

			ctx := pinpoint.NewContext(req.Context(), tracer)
			c.SetRequest(req.WithContext(ctx))
			defer tracer.NewSpanEvent(req.Method + " " + c.Path()).EndSpanEvent()

			err := next(c)
			if err != nil {
				tracer.Span().SetError(err)
				c.Error(err)
			}

			phttp.TraceHttpStatus(tracer, c.Response().Status)
			return err

		}
	}
}
