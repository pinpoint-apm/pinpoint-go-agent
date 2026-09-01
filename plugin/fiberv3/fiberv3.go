// Package ppfiberv3 instruments the gofiber/fiber/v3 package (https://github.com/gofiber/fiber).
//
// This package instruments inbound requests handled by a fiber instance.
// Register the Middleware as the middleware of the router to trace all handlers:
//
//	app := fiber.New()
//	app.Use(ppfiberv3.Middleware())
//
// Use WrapHandler to select the handlers you want to track:
//
//	app.Get("/hello", ppfiberv3.WrapHandler(hello))
package ppfiberv3

import (
	"errors"
	"net/http"

	"github.com/gofiber/fiber/v3"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	ppfasthttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

const serverName = "Fiber Server"

// Middleware returns middleware that will trace incoming requests.
func Middleware() fiber.Handler {
	return wrap(func(c fiber.Ctx) error { return c.Next() }, "fiber.HandlerFunc()")
}

// WrapHandler wraps the given fiber handler and adds the pinpoint.Tracer to the request context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
func WrapHandler(handler fiber.Handler) fiber.Handler {
	return wrap(func(c fiber.Ctx) error { return handler(c) }, pphttp.HandlerFuncName(handler))
}

func wrap(f func(c fiber.Ctx) error, handlerName string) fiber.Handler {
	return func(c fiber.Ctx) error {
		if !pinpoint.GetAgent().Enable() {
			return f(c)
		}

		method := string(c.RequestCtx().Method())
		status := http.StatusOK
		tracer := pphttp.NewHttpServerTracerWithReader(
			method,
			string(c.RequestCtx().Path()),
			serverName,
			ppfasthttp.RequestHeader{Hdr: &c.RequestCtx().Request.Header},
		)
		// Record straight from the fasthttp request: converting it to a
		// net/http request (fasthttpadaptor.ConvertRequest) materialized the
		// full header map, parsed the URL and buffered the body per sampled
		// request, only for values the default noop recorders never read.
		// The sampling check keeps the host copy and remote-addr formatting
		// off the unsampled path; the callee would discard them.
		if tracer.IsSampled() {
			pphttp.RecordHttpServerRequestWithReader(tracer,
				string(c.RequestCtx().Host()), c.RequestCtx().RemoteAddr().String(),
				ppfasthttp.RequestHeader{Hdr: &c.RequestCtx().Request.Header}, ppfasthttp.Cookie{Hdr: &c.RequestCtx().Request.Header})
		}

		defer tracer.EndSpan()
		defer func() {
			pphttp.CollectUrlStat(tracer, c.Route().Path, method, status)
			recordResponse(tracer, c, status)
		}()
		defer func() {
			if e := recover(); e != nil {
				status = http.StatusInternalServerError
				panic(e)
			}
		}()

		defer tracer.NewSpanEvent(handlerName).EndSpanEvent()

		// Derive from the request's own context, not a fresh background one:
		// replacing it discarded whatever an earlier middleware had put there
		// - auth values, deadlines - for the rest of the handler.
		c.SetContext(pinpoint.NewContext(c.Context(), tracer))
		err := f(c)
		if err != nil {
			pphttp.RecordHttpHandlerError(tracer, err)
			status = statusCode(err)
		} else {
			status = c.Response().StatusCode()
		}
		return err
	}
}

func recordResponse(tracer pinpoint.Tracer, c fiber.Ctx, status int) {
	pphttp.RecordHttpServerResponseWithReader(tracer, status, ppfasthttp.ResponseHeader{Hdr: &c.RequestCtx().Response.Header})
}

func statusCode(err error) int {
	var e *fiber.Error
	code := fiber.StatusInternalServerError
	if errors.As(err, &e) {
		code = e.Code
	}
	return code
}
