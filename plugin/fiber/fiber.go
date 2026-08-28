// Package ppfiber instruments the gofiber/fiber/v2 package (https://github.com/gofiber/fiber).
//
// This package instruments inbound requests handled by a fiber instance.
// Register the Middleware as the middleware of the router to trace all handlers:
//
//	app := fiber.New()
//	app.Use(ppfiber.Middleware())
//
// Use WrapHandler to select the handlers you want to track:
//
//	app.Get("/hello", ppfiber.WrapHandler(hello))
package ppfiber

import (
	"errors"
	"net/http"

	"github.com/gofiber/fiber/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"github.com/valyala/fasthttp"
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

		method := string(c.Context().Method())
		status := http.StatusOK
		tracer := pphttp.NewHttpServerTracerWithReader(
			method,
			string(c.Context().Path()),
			serverName,
			fiberRequestHeader{&c.Context().Request.Header},
		)
		// Record straight from the fasthttp request: converting it to a
		// net/http request (fasthttpadaptor.ConvertRequest) materialized the
		// full header map, parsed the URL and buffered the body per sampled
		// request, only for values the default noop recorders never read.
		pphttp.RecordHttpServerRequestWithReader(tracer,
			string(c.Context().Host()), c.Context().RemoteAddr().String(),
			fiberRequestHeader{&c.Context().Request.Header}, fiberCookie{&c.Context().Request.Header})

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

		// Derive from the request's own user context, not a fresh background
		// one: replacing it discarded whatever an earlier middleware had put
		// there - auth values, deadlines - for the rest of the handler.
		c.SetUserContext(pinpoint.NewContext(c.UserContext(), tracer))
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

type fiberRequestHeader struct {
	header *fasthttp.RequestHeader
}

func (h fiberRequestHeader) Get(key string) string {
	return string(h.header.Peek(key))
}

func (h fiberRequestHeader) Values(key string) []string {
	return []string{string(h.header.Peek(key))}
}

func (h fiberRequestHeader) VisitAll(f func(name string, values []string)) {
	h.header.VisitAll(func(key, value []byte) {
		f(string(key), []string{string(value)})
	})
}

type fiberCookie struct {
	header *fasthttp.RequestHeader
}

func (c fiberCookie) VisitAll(f func(name string, value string)) {
	c.header.VisitAllCookie(func(key, value []byte) {
		f(string(key), string(value))
	})
}

type fiberResponseHeader struct {
	header *fasthttp.ResponseHeader
}

func (h fiberResponseHeader) Values(key string) []string {
	return []string{string(h.header.Peek(key))}
}

func (h fiberResponseHeader) VisitAll(f func(name string, values []string)) {
	h.header.VisitAll(func(key, value []byte) {
		f(string(key), []string{string(value)})
	})
}

func recordResponse(tracer pinpoint.Tracer, c *fiber.Ctx, status int) {
	pphttp.RecordHttpServerResponseWithReader(tracer, status, fiberResponseHeader{&c.Context().Response.Header})
}

func statusCode(err error) int {
	var e *fiber.Error
	code := fiber.StatusInternalServerError
	if errors.As(err, &e) {
		code = e.Code
	}
	return code
}
