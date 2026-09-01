// Package ppgorilla instruments the gorilla/mux package (https://github.com/gorilla/mux).
//
// This package instruments inbound requests handled by a gorilla mux.Router.
// Register the Middleware as the middleware of the router to trace all handlers:
//
//	r := mux.NewRouter()
//	r.Use(ppgorilla.Middleware())
//
// Use WrapHandler or WrapHandlerFunc to select the handlers you want to track:
//
//	r.HandleFunc("/outgoing", ppgorilla.WrapHandlerFunc(outGoing))
package ppgorilla

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

const serverName = "Gorilla/Mux HTTP Server"

// Middleware returns a mux middleware that creates a pinpoint.Tracer that instruments the http handler function.
func Middleware() mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return wrap(next, "gorilla/mux.HandlerFunc()")
	}
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

// routePattern returns the gorilla/mux path template of r, or "" when there
// is none - the handler may run outside a mux router.
func routePattern(r *http.Request) string {
	if route := mux.CurrentRoute(r); route != nil {
		path, _ := route.GetPathTemplate()
		return path
	}
	return ""
}

func wrap(handler http.Handler, funcName string) http.Handler {
	return pphttp.TraceHandler(handler, serverName, funcName, routePattern)
}
