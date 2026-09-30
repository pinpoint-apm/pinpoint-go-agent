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
	ppfasthttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
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
		tracer := serverTracer(c, method)

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

// serverTracer starts the request's span, or hands back a nested view of the
// tracer the user context already carries (pinpoint.NestedTracer) - Middleware
// and WrapHandler on the same route, or the middleware registered twice - so
// one request makes one span, as pphttp.NewHttpServerTracer does: this layer's
// event goes on the existing span and its EndSpan is ignored.
func serverTracer(c *fiber.Ctx, method string) pinpoint.Tracer {
	if existing := pinpoint.FromContext(c.UserContext()); existing != pinpoint.NoopTracer() {
		return pinpoint.NestedTracer(existing)
	}
	tracer := pphttp.NewHttpServerTracerWithReader(
		method,
		string(c.Context().Path()),
		serverName,
		ppfasthttp.HeaderReader{Hdr: &c.Context().Request.Header},
	)
	// Record straight from the fasthttp request: converting it to a
	// net/http request (fasthttpadaptor.ConvertRequest) materialized the
	// full header map, parsed the URL and buffered the body per sampled
	// request, only for values the default noop recorders never read.
	// The sampling check keeps the host copy and remote-addr formatting
	// off the unsampled path; the callee would discard them.
	if tracer.IsSampled() {
		pphttp.RecordHttpServerRequestWithReader(tracer,
			string(c.Context().Host()), c.Context().RemoteAddr().String(),
			ppfasthttp.RequestHeader{Hdr: &c.Context().Request.Header}, ppfasthttp.Cookie{Hdr: &c.Context().Request.Header})
	}
	return tracer
}

func recordResponse(tracer pinpoint.Tracer, c *fiber.Ctx, status int) {
	pphttp.RecordHttpServerResponseWithReader(tracer, status, ppfasthttp.ResponseHeader{Hdr: &c.Context().Response.Header})
}

func statusCode(err error) int {
	var e *fiber.Error
	code := fiber.StatusInternalServerError
	if errors.As(err, &e) {
		code = e.Code
	}
	return code
}
