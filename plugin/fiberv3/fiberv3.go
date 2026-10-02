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

	"github.com/gofiber/fiber/v3"
	ppfasthttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
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
		tracer := serverTracer(c, method)
		// Derive from the request's own context, not a fresh background one:
		// replacing it discarded whatever an earlier middleware had put there
		// - auth values, deadlines - for the rest of the handler.
		c.SetContext(pinpoint.NewContext(c.Context(), tracer))
		var err error
		pphttp.TraceSpan(tracer, handlerName, func() int {
			if err = f(c); err != nil {
				pphttp.RecordHttpHandlerError(tracer, err)
				return statusCode(err)
			}
			return c.Response().StatusCode()
		}, func(status int) {
			pphttp.CollectUrlStat(tracer, c.Route().Path, method, status)
			ppfasthttp.RecordServerResponse(tracer, c.RequestCtx(), status)
		})
		return err
	}
}

// serverTracer starts the request's span, or hands back a nested view of the
// tracer the request context already carries (pinpoint.NestedTracer) -
// Middleware and WrapHandler on the same route, or the middleware registered
// twice - so one request makes one span, as pphttp.NewHttpServerTracer does:
// this layer's event goes on the existing span and its EndSpan is ignored.
func serverTracer(c fiber.Ctx, method string) pinpoint.Tracer {
	if existing := pinpoint.FromContext(c.Context()); existing != pinpoint.NoopTracer() {
		return pinpoint.NestedTracer(existing)
	}
	return ppfasthttp.NewServerTracer(c.RequestCtx(), method, serverName)
}

func statusCode(err error) int {
	var e *fiber.Error
	code := fiber.StatusInternalServerError
	if errors.As(err, &e) {
		code = e.Code
	}
	return code
}
