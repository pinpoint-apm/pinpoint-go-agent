// Package ppechov5 instruments the labstack/echo/v5 package (https://github.com/labstack/echo).
//
// This package instruments inbound requests handled by a echo.Router.
// Register the Middleware as the middleware of the router to trace all handlers:
//
//	e := echo.New()
//	e.Use(ppechov5.Middleware())
//
// Use WrapHandler to select the handlers you want to track:
//
//	e.GET("/hello", ppechov5.WrapHandler(hello))
package ppechov5

import (
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

const serverName = "Echo HTTP Server"

func wrap(handler echo.HandlerFunc, funcName string) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if !pinpoint.GetAgent().Enable() {
			return handler(c)
		}

		status := http.StatusOK
		req := c.Request()
		tracer := pphttp.NewHttpServerTracer(req, serverName)

		defer tracer.EndSpan()
		defer func() {
			pphttp.CollectUrlStat(tracer, c.Path(), req.Method, status)
			pphttp.RecordHttpServerResponse(tracer, status, c.Response().Header())
		}()
		defer func() {
			if e := recover(); e != nil {
				status = http.StatusInternalServerError
				panic(e)
			}
		}()
		defer tracer.NewSpanEvent(funcName).EndSpanEvent()

		ctx := pinpoint.NewContext(req.Context(), tracer)
		c.SetRequest(req.WithContext(ctx))
		err := handler(c)
		if err != nil {
			pphttp.RecordHttpHandlerError(tracer, err)
		}
		// Do not call c.Error here: returning the error already routes it to
		// echo's HTTPErrorHandler, and calling it as well would run that
		// handler - and its logging, metrics and other side effects - twice for
		// every failed request. ResolveResponseStatus reports the status echo
		// will send without running the error handler.
		_, status = echo.ResolveResponseStatus(c.Response(), err)
		return err
	}
}

// Middleware returns an echo middleware that creates a pinpoint.Tracer that instruments the echo handler function.
func Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		// v5 no longer records the handler function name on a route
		// (echo.RouteInfo.Name defaults to "METHOD:/path"), so the middleware
		// cannot name the wrapped handler the way ppechov4 did. Use a fixed
		// name, as the gin and fiber plugins do. WrapHandler still reports the
		// real function name.
		return wrap(next, "echo.HandlerFunc()")
	}
}

// WrapHandler wraps the given echo handler and adds the pinpoint.Tracer to the request's context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
func WrapHandler(handler echo.HandlerFunc) echo.HandlerFunc {
	return wrap(handler, pphttp.HandlerFuncName(handler))
}
