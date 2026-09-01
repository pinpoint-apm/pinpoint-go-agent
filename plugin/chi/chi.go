// Package ppchi instruments the go-chi/chi package (https://github.com/go-chi/chi).
//
// This package instruments inbound requests handled by a chi.Router.
// Register the Middleware as the middleware of the router to trace all handlers:
//
//	r := chi.NewRouter()
//	r.Use(ppchi.Middleware())
//
// Use WrapHandler or WrapHandlerFunc to select the handlers you want to track:
//
//	r.Get("/hello", ppchi.WrapHandlerFunc(hello))
package ppchi

import (
	"github.com/go-chi/chi/v5"
	"net/http"

	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

const serverName = "Chi HTTP Server"

// Middleware returns a chi middleware that creates a pinpoint.Tracer that instruments the http handler.
func Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return wrap(next, "chi.HandlerFunc()")
	}
}

// routePattern returns the chi route pattern of r, or "" when there is none.
// chi.RouteContext returns nil whenever the handler runs outside a chi router -
// mounted on a plain ServeMux, or invoked directly - so the result cannot be
// dereferenced blindly.
func routePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		return rctx.RoutePattern()
	}
	return ""
}

func wrap(handler http.Handler, funcName string) http.Handler {
	return pphttp.TraceHandler(handler, serverName, funcName, routePattern)
}

// WrapHandler wraps the given http handler.
func WrapHandler(handler http.Handler) http.Handler {
	return wrap(handler, pphttp.HandlerFuncName(handler))
}

// WrapHandlerFunc wraps the given http handler function.
func WrapHandlerFunc(f func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	h := WrapHandler(http.HandlerFunc(f))
	return func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }
}
