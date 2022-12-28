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
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"net/http"
)

const serverName = "Gin HTTP Server"

// Middleware returns a gin middleware that creates a pinpoint.Tracer that instruments the gin handler function.
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !pinpoint.GetAgent().Enable() {
			c.Next()
			return
		}

		status := http.StatusOK
		tracer := pphttp.NewHttpServerTracer(c.Request, serverName)
		defer tracer.EndSpan()
		defer tracer.CollectUrlStat(c.FullPath(), &status)

		defer func() {
			if e := recover(); e != nil {
				status = http.StatusInternalServerError
				pphttp.RecordHttpServerResponse(tracer, status, c.Writer.Header())
				panic(e)
			}
		}()

		tracer.NewSpanEvent("gin.HandlerFunc()")
		defer tracer.EndSpanEvent()

		c.Request = pinpoint.RequestWithTracerContext(c.Request, tracer)
		c.Next()
		if len(c.Errors) > 0 {
			tracer.Span().SetError(c.Errors.Last())
		}

		status = c.Writer.Status()
		pphttp.RecordHttpServerResponse(tracer, status, c.Writer.Header())
	}
}

// WrapHandler wraps the given gin handler and adds the pinpoint.Tracer to the request's context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
func WrapHandler(handler gin.HandlerFunc) gin.HandlerFunc {
	funcName := pphttp.HandlerFuncName(handler)

	return func(c *gin.Context) {
		if !pinpoint.GetAgent().Enable() {
			handler(c)
			return
		}

		status := http.StatusOK
		tracer := pphttp.NewHttpServerTracer(c.Request, serverName)
		defer tracer.EndSpan()
		defer tracer.CollectUrlStat(c.FullPath(), &status)

		defer func() {
			if e := recover(); e != nil {
				status = http.StatusInternalServerError
				pphttp.RecordHttpServerResponse(tracer, status, c.Writer.Header())
				panic(e)
			}
		}()

		tracer.NewSpanEvent(funcName)
		defer tracer.EndSpanEvent()

		c.Request = pinpoint.RequestWithTracerContext(c.Request, tracer)
		handler(c)
		if len(c.Errors) > 0 {
			tracer.Span().SetError(c.Errors.Last())
		}
		status = c.Writer.Status()
		pphttp.RecordHttpServerResponse(tracer, status, c.Writer.Header())
	}
}
