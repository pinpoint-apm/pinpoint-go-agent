// Package ppecho instruments the labstack/echo package (https://github.com/labstack/echo).
//
// This package instruments inbound requests handled by a echo.Router.
// Register the Middleware as the middleware of the router to trace all handlers:
//
//	e := echo.New()
//	e.Use(ppecho.Middleware())
//
// Use WrapHandler to select the handlers you want to track:
//
//	e.GET("/hello", ppecho.WrapHandler(hello))
//
// echo v3 is end-of-life upstream, and GHSA-vfp3-v2gw-7wfq - an encoded-slash
// bypass of route-level protection that exposes static files - covers every v3
// release including the last, v3.3.10. No patched v3 exists. The vulnerable
// code is not reachable from this package, but an application pinning echo v3
// inherits it; plugin/echov4 and plugin/echov5 are the upgrade paths.
package ppecho

import (
	"errors"
	"net/http"
	"sync"

	"github.com/labstack/echo"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

const serverName = "Echo HTTP Server"

type key struct {
	method string
	path   string
}

var (
	handlerNameMap map[key]string
	once           sync.Once
)

func makeHandlerNameMap(c echo.Context) {
	handlerNameMap = make(map[key]string, 0)
	for _, r := range c.Echo().Routes() {
		k := key{r.Method, r.Path}
		handlerNameMap[k] = r.Name + "()"
	}
}

func handlerName(c echo.Context, r *http.Request) string {
	once.Do(func() {
		makeHandlerNameMap(c)
	})

	k := key{r.Method, c.Path()}
	if name, ok := handlerNameMap[k]; ok {
		return name
	} else {
		return "echo.HandlerFunc()"
	}
}

func wrap(handler echo.HandlerFunc, funcName string) echo.HandlerFunc {
	return func(c echo.Context) error {
		if !pinpoint.GetAgent().Enable() {
			return handler(c)
		}

		status := http.StatusOK
		req := c.Request()
		tracer := pphttp.NewHttpServerTracer(req, serverName)

		defer tracer.EndSpan()
		defer func() {
			pphttp.CollectUrlStat(tracer, c.Path(), req.Method, status)
			pphttp.RecordHttpServerResponse(tracer, status, c.Response().Header())
		}()
		defer func() {
			if e := recover(); e != nil {
				status = http.StatusInternalServerError
				panic(e)
			}
		}()

		if funcName == "" {
			funcName = handlerName(c, req)
		}
		defer tracer.NewSpanEvent(funcName).EndSpanEvent()

		ctx := pinpoint.NewContext(req.Context(), tracer)
		c.SetRequest(req.WithContext(ctx))
		err := handler(c)
		if err != nil {
			pphttp.RecordHttpHandlerError(tracer, err)
			// Do not call c.Error here: returning the error already routes it
			// to echo's HTTPErrorHandler, and calling it as well ran that
			// handler - and its logging, metrics and other side effects -
			// twice for every failed request. Derive the status from the
			// error instead of reading it off the not-yet-committed response,
			// as the fiber plugin does.
			status = statusCode(err)
		} else {
			status = c.Response().Status
		}
		return err
	}
}

// Middleware returns an echo middleware that creates a pinpoint.Tracer that instruments the echo handler function.
func Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return wrap(next, "")
	}
}

// WrapHandler wraps the given echo handler and adds the pinpoint.Tracer to the request's context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
func WrapHandler(handler echo.HandlerFunc) echo.HandlerFunc {
	return wrap(handler, pphttp.HandlerFuncName(handler))
}

// statusCode reports the status echo's HTTPErrorHandler will send for err.
func statusCode(err error) int {
	var e *echo.HTTPError
	if errors.As(err, &e) {
		return e.Code
	}
	return http.StatusInternalServerError
}
