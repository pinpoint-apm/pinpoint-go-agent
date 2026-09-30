// Package ppgin instruments the gin-gonic/gin package (https://github.com/gin-gonic/gin).
//
// This package instruments inbound requests handled by a gin.Engine.
// Register the Middleware as the middleware of the router to trace all handlers:
//
//	r := gin.Default()
//	r.Use(ppgin.Middleware())
//
// Use WrapHandler to select the handlers you want to track:
//
//	r.GET("/external", ppgin.WrapHandler(extCall))
package ppgin

import (
	"github.com/gin-gonic/gin"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

const serverName = "Gin HTTP Server"

// Middleware returns a gin middleware that creates a pinpoint.Tracer that instruments the gin handler function.
func Middleware() gin.HandlerFunc {
	return wrap(func(c *gin.Context) { c.Next() }, "gin.HandlerFunc()")
}

func wrap(doFunc func(c *gin.Context), funcName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !pinpoint.GetAgent().Enable() {
			doFunc(c)
			return
		}

		tracer := pphttp.NewHttpServerTracer(c.Request, serverName)
		c.Request = pinpoint.RequestWithTracerContext(c.Request, tracer)
		pphttp.TraceSpan(tracer, funcName, func() int {
			doFunc(c)
			if len(c.Errors) > 0 {
				pphttp.RecordHttpHandlerError(tracer, c.Errors.Last())
			}
			return c.Writer.Status()
		}, func(status int) {
			pphttp.CollectUrlStat(tracer, c.FullPath(), c.Request.Method, status)
			pphttp.RecordHttpServerResponse(tracer, status, c.Writer.Header())
		})
	}
}

// WrapHandler wraps the given gin handler and adds the pinpoint.Tracer to the request's context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
func WrapHandler(handler gin.HandlerFunc) gin.HandlerFunc {
	return wrap(func(c *gin.Context) { handler(c) }, pphttp.HandlerFuncName(handler))
}
