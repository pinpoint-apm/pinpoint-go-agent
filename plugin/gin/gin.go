package gin

import (
	"github.com/gin-gonic/gin"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"net/http"
)

const serverName = "Gin HTTP Server"

func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !pinpoint.GetAgent().Enable() {
			c.Next()
			return
		}

		tracer := phttp.NewHttpServerTracer(c.Request, serverName)
		defer tracer.EndSpan()

		if !tracer.IsSampled() {
			c.Next()
			return
		}
		defer func() {
			if e := recover(); e != nil {
				status := http.StatusInternalServerError
				phttp.RecordHttpServerResponse(tracer, status, c.Writer.Header())
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

		phttp.RecordHttpServerResponse(tracer, c.Writer.Status(), c.Writer.Header())
	}
}

func WrapHandler(handler gin.HandlerFunc) gin.HandlerFunc {
	funcName := phttp.HandlerFuncName(handler)

	return func(c *gin.Context) {
		if !pinpoint.GetAgent().Enable() {
			handler(c)
			return
		}

		tracer := phttp.NewHttpServerTracer(c.Request, serverName)
		defer tracer.EndSpan()

		if !tracer.IsSampled() {
			handler(c)
			return
		}
		defer func() {
			if e := recover(); e != nil {
				status := http.StatusInternalServerError
				phttp.RecordHttpServerResponse(tracer, status, c.Writer.Header())
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
		phttp.RecordHttpServerResponse(tracer, c.Writer.Status(), c.Writer.Header())
	}
}
