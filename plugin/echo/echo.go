// Package ppecho instruments the labstack/echo package (https://github.com/labstack/echo).
//
// This package instruments inbound requests handled by a echo.Router.
// Register the Middleware as the middleware of the router to trace all handlers:
//
//	e := echo.New()
//	e.Use(ppecho.Middleware())
//
// Use WrapHandler to select the handlers you want to track:
//
//	e.GET("/hello", ppecho.WrapHandler(hello))
package ppecho

import (
	"net/http"

	"github.com/labstack/echo"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

const serverName = "Echo HTTP Server"

// Middleware returns an echo middleware that creates a pinpoint.Tracer that instruments the echo handler function.
func Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !pinpoint.GetAgent().Enable() {
				return next(c)
			}

			req := c.Request()
			tracer := pphttp.NewHttpServerTracer(req, serverName)
			defer tracer.EndSpan()

			if !tracer.IsSampled() {
				return next(c)
			}
			defer func() {
				if e := recover(); e != nil {
					status := http.StatusInternalServerError
					pphttp.RecordHttpServerResponse(tracer, status, c.Response().Header())
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

			pphttp.RecordHttpServerResponse(tracer, c.Response().Status, c.Response().Header())
			return err
		}
	}
}

// WrapHandler wraps the given echo handler and adds the pinpoint.Tracer to the request's context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
func WrapHandler(handler echo.HandlerFunc) echo.HandlerFunc {
	funcName := pphttp.HandlerFuncName(handler)

	return func(c echo.Context) error {
		if !pinpoint.GetAgent().Enable() {
			return handler(c)
		}

		req := c.Request()
		tracer := pphttp.NewHttpServerTracer(req, serverName)
		defer tracer.EndSpan()

		if !tracer.IsSampled() {
			return handler(c)
		}
		defer func() {
			if e := recover(); e != nil {
				status := http.StatusInternalServerError
				pphttp.RecordHttpServerResponse(tracer, status, c.Response().Header())
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

		pphttp.RecordHttpServerResponse(tracer, c.Response().Status, c.Response().Header())
		return err
	}
}
