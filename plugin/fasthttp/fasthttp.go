// Package ppfasthttp instruments the valyala/fasthttp package (https://github.com/valyala/fasthttp).
//
// This package instruments inbound requests handled by a fasthttp instance.
// Register the Middleware as the middleware of the router to trace all handlers:
//
package ppfasthttp

import (
	"context"
	"net/http"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
)

const serverName = "FastHttp Server"
const CtxKey = "pinpoint"

func WrapHandler(handler fasthttp.RequestHandler) fasthttp.RequestHandler {
	handlerName := pphttp.HandlerFuncName(handler)

	return func(ctx *fasthttp.RequestCtx) {
		if !pinpoint.GetAgent().Enable() {
			handler(ctx)
			return
		}

		req := new(http.Request)
		if err := fasthttpadaptor.ConvertRequest(ctx, req, true); err != nil {
			handler(ctx)
			return
		}

		tracer := pphttp.NewHttpServerTracer(req, serverName)
		defer tracer.EndSpan()

		if !tracer.IsSampled() {
			handler(ctx)
			return
		}
		defer func() {
			if e := recover(); e != nil {
				status := http.StatusInternalServerError
				recordResponse(tracer, ctx, status)
				panic(e)
			}
		}()

		defer tracer.NewSpanEvent(handlerName).EndSpanEvent()

		ctx.SetUserValue(CtxKey, pinpoint.NewContext(context.Background(), tracer))
		handler(ctx)
		tracer.Span().SetError(ctx.Err())

		recordResponse(tracer, ctx, ctx.Response.StatusCode())
	}
}

func recordResponse(tracer pinpoint.Tracer, c *fasthttp.RequestCtx, status int) {
	if tracer.IsSampled() {
		h := make(http.Header)
		c.Response.Header.VisitAll(func(k, v []byte) {
			h.Set(string(k), string(v))
		})
		pphttp.RecordHttpServerResponse(tracer, status, h)
	}
}
