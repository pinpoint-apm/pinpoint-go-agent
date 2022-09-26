package echo

import (
	"github.com/labstack/echo"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"net/http"
)

const serverName = "Echo HTTP Server"

func Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !pinpoint.GetAgent().Enable() {
				return next(c)
			}

			req := c.Request()
			tracer := phttp.NewHttpServerTracer(req, serverName)
			defer tracer.EndSpan()

			if !tracer.IsSampled() {
				return next(c)
			}
			defer func() {
				if e := recover(); e != nil {
					status := http.StatusInternalServerError
					phttp.RecordHttpServerResponse(tracer, status, c.Response().Header())
					panic(e)
				}
			}()

			tracer.NewSpanEvent("echo.HandlerFunc()")
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

func WrapHandler(handler echo.HandlerFunc) echo.HandlerFunc {
	funcName := phttp.HandlerFuncName(handler)

	return func(c echo.Context) error {
		if !pinpoint.GetAgent().Enable() {
			return handler(c)
		}

		req := c.Request()
		tracer := phttp.NewHttpServerTracer(req, serverName)
		defer tracer.EndSpan()

		if !tracer.IsSampled() {
			return handler(c)
		}
		defer func() {
			if e := recover(); e != nil {
				status := http.StatusInternalServerError
				phttp.RecordHttpServerResponse(tracer, status, c.Response().Header())
				panic(e)
			}
		}()

		tracer.NewSpanEvent(funcName)
		defer tracer.EndSpanEvent()

		ctx := pinpoint.NewContext(req.Context(), tracer)
		c.SetRequest(req.WithContext(ctx))
		err := handler(c)
		if err != nil {
			tracer.Span().SetError(err)
			c.Error(err)
		}

		phttp.RecordHttpServerResponse(tracer, c.Response().Status, c.Response().Header())
		return err
	}
}
