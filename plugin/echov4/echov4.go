// Package ppechov4 instruments the labstack/echo/v4 package (https://github.com/labstack/echo).
//
// This package instruments inbound requests handled by a echo.Router.
// Register the Middleware as the middleware of the router to trace all handlers:
//
//	e := echo.New()
//	e.Use(ppechov4.Middleware())
//
// Use WrapHandler to select the handlers you want to track:
//
//	e.GET("/hello", ppechov4.WrapHandler(hello))
package ppechov4

import (
	"net/http"
	"sync/atomic"

	"github.com/labstack/echo/v4"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

const serverName = "Echo HTTP Server"

type key struct {
	method string
	path   string
}

func makeHandlerNameMap(e *echo.Echo) map[key]string {
	names := make(map[key]string)
	for _, r := range e.Routes() {
		names[key{r.Method, r.Path}] = r.Name + "()"
	}
	return names
}

// echoNames is the route table of one Echo, built from its Routes() on the
// first sampled request a Middleware serves. Per instance rather than once per
// process: a second Echo in the same process, a test suite's instances, or an
// instance built after the first request all reported "echo.HandlerFunc()"
// for every route under a single process-wide table. A middleware's requests
// all come from the Echo it is registered on, so the one it holds is that
// Echo's; one shared across instances rebuilds the table as it switches.
type echoNames struct {
	e     *echo.Echo
	names map[key]string
}

func handlerName(c echo.Context, r *http.Request, last *atomic.Pointer[echoNames]) string {
	e := c.Echo()
	l := last.Load()
	if l == nil || l.e != e {
		l = &echoNames{e, makeHandlerNameMap(e)}
		last.Store(l)
	}

	if name, ok := l.names[key{r.Method, c.Path()}]; ok {
		return name
	}
	return "echo.HandlerFunc()"
}

func wrap(handler echo.HandlerFunc, funcName string, last *atomic.Pointer[echoNames]) echo.HandlerFunc {
	return func(c echo.Context) error {
		if !pinpoint.GetAgent().Enable() {
			return handler(c)
		}

		req := c.Request()
		tracer := pphttp.NewHttpServerTracer(req, serverName)

		// A local, never the captured parameter: Middleware passes "" once and
		// the closure serves every request, so writing the resolved name back
		// pinned the first request's handler name onto every later route and
		// raced between concurrent requests.
		name := funcName
		if name == "" {
			// An unsampled span discards the name, so the route table is not
			// consulted for it, as echov5 does.
			name = "echo.HandlerFunc()"
			if tracer.IsSampled() {
				name = handlerName(c, req, last)
			}
		}

		c.SetRequest(pinpoint.RequestWithTracerContext(req, tracer))
		var err error
		pphttp.TraceSpan(tracer, name, func() int {
			if err = handler(c); err == nil {
				return c.Response().Status
			}
			// Do not call c.Error here: returning the error already routes it
			// to echo's HTTPErrorHandler, and calling it as well ran that
			// handler - and its logging, metrics and other side effects -
			// twice for every failed request. Derive the status from the
			// error instead of reading it off the not-yet-committed response,
			// as the fiber plugin does - unless the response is committed:
			// echo's error handler then leaves it alone, and the wire keeps
			// the status the handler already wrote.
			pphttp.RecordHttpHandlerError(tracer, err)
			if c.Response().Committed {
				return c.Response().Status
			}
			return statusCode(err)
		}, func(status int) {
			pphttp.CollectUrlStat(tracer, c.Path(), req.Method)
			pphttp.RecordHttpServerResponse(tracer, status, c.Response().Header())
		})
		return err
	}
}

// Middleware returns an echo middleware that creates a pinpoint.Tracer that instruments the echo handler function.
func Middleware() echo.MiddlewareFunc {
	// Held here, not in wrap: echo applies its middleware to the handler on
	// every request, so a cache made per wrap was new on every request and
	// never hit, and each sampled request looked the route table up again.
	var last atomic.Pointer[echoNames]
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return wrap(next, "", &last)
	}
}

// WrapHandler wraps the given echo handler and adds the pinpoint.Tracer to the request's context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
func WrapHandler(handler echo.HandlerFunc) echo.HandlerFunc {
	// The name is the handler's own, so the route table is never consulted.
	return wrap(handler, pphttp.HandlerFuncName(handler), nil)
}

// statusCode follows Echo's default error handler, including its Internal override.
func statusCode(err error) int {
	if e, ok := err.(*echo.HTTPError); ok {
		if internal, ok := e.Internal.(*echo.HTTPError); ok {
			e = internal
		}
		return e.Code
	}
	return http.StatusInternalServerError
}
