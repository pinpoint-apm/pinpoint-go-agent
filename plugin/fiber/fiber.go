// Package ppfiber instruments the gofiber/fiber/v2 package (https://github.com/gofiber/fiber).
//
// This package instruments inbound requests handled by a fiber instance.
// Register the Middleware as the middleware of the router to trace all handlers:
//
//  app := fiber.New()
//  app.Use(ppfiber.Middleware())
//
// Use WrapHandler to select the handlers you want to track:
//
//	app.Get("/hello", ppfiber.WrapHandler(hello))
package ppfiber

import (
	"context"
	"errors"
	"net/http"

	"github.com/gofiber/fiber/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"github.com/valyala/fasthttp/fasthttpadaptor"
)

const serverName = "Fiber Server"

// Middleware returns middleware that will trace incoming requests.
func Middleware() func(c *fiber.Ctx) error {
	return wrap(func(c *fiber.Ctx) error { return c.Next() }, "fiber.HandlerFunc()")
}

// WrapHandler wraps the given fiber handler and adds the pinpoint.Tracer to the user context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
func WrapHandler(handler fiber.Handler) fiber.Handler {
	return wrap(func(c *fiber.Ctx) error { return handler(c) }, pphttp.HandlerFuncName(handler))
}

func wrap(f func(c *fiber.Ctx) error, handlerName string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if !pinpoint.GetAgent().Enable() {
			return f(c)
		}

		req := new(http.Request)
		if err := fasthttpadaptor.ConvertRequest(c.Context(), req, true); err != nil {
			return f(c)
		}

		status := http.StatusOK
		tracer := pphttp.NewHttpServerTracer(req, serverName)

		defer tracer.EndSpan()
		defer func() {
			tracer.CollectUrlStat(c.Route().Path, status)
			recordResponse(tracer, c, status)
		}()
		defer func() {
			if e := recover(); e != nil {
				status = http.StatusInternalServerError
				panic(e)
			}
		}()

		defer tracer.NewSpanEvent(handlerName).EndSpanEvent()

		c.SetUserContext(pinpoint.NewContext(context.Background(), tracer))
		err := f(c)
		if err != nil {
			tracer.Span().SetError(err)
			status = statusCode(err)
		} else {
			status = c.Response().StatusCode()
		}
		return err
	}
}

func recordResponse(tracer pinpoint.Tracer, c *fiber.Ctx, status int) {
	if tracer.IsSampled() {
		h := make(http.Header)
		c.Context().Response.Header.VisitAll(func(k, v []byte) {
			h.Set(string(k), string(v))
		})
		pphttp.RecordHttpServerResponse(tracer, status, h)
	}
}

func statusCode(err error) int {
	var e *fiber.Error
	code := fiber.StatusInternalServerError
	if errors.As(err, &e) {
		code = e.Code
	}
	return code
}
