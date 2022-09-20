package echo

import (
	"github.com/labstack/echo"

	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !pinpoint.GetAgent().Enable() {
				return next(c)
			}

			req := c.Request()
			tracer := phttp.NewHttpServerTracer(req, "Echo Server")
			defer tracer.EndSpan()

			if !tracer.IsSampled() {
				return next(c)
			}

			tracer.NewSpanEvent("echo.HandlerFunc(echo.Context)")
			defer tracer.EndSpanEvent()

			ctx := pinpoint.NewContext(req.Context(), tracer)
			c.SetRequest(req.WithContext(ctx))
			err := next(c)
			if err != nil {
				tracer.Span().SetError(err)
				c.Error(err)
			}

			phttp.RecordHttpServerResponse(tracer, c.Response().Status, c.Response().Header())
			return err
		}
	}
}
