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
	"net/http"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

const serverName = "Chi HTTP Server"

// Middleware returns a chi middleware that creates a pinpoint.Tracer that instruments the http handler.
func Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !pinpoint.GetAgent().Enable() {
				next.ServeHTTP(w, r)
				return
			}

			tracer := pphttp.NewHttpServerTracer(r, serverName)
			defer tracer.EndSpan()

			if !tracer.IsSampled() {
				next.ServeHTTP(w, r)
				return
			}
			defer func() {
				if e := recover(); e != nil {
					status := http.StatusInternalServerError
					pphttp.RecordHttpServerResponse(tracer, status, w.Header())
					panic(e)
				}
			}()

			tracer.NewSpanEvent("chi.HandlerFunc()")
			defer tracer.EndSpanEvent()

			status := http.StatusOK
			w = pphttp.WrapResponseWriter(w, &status)
			r = pinpoint.RequestWithTracerContext(r, tracer)

			next.ServeHTTP(w, r)
			pphttp.RecordHttpServerResponse(tracer, status, w.Header())
		})
	}
}

// WrapHandler wraps the given http handler.
func WrapHandler(handler http.Handler) http.Handler {
	return pphttp.WrapHandler(handler, serverName)
}

// WrapHandlerFunc wraps the given http handler function.
func WrapHandlerFunc(f func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	return pphttp.WrapHandlerFunc(f, serverName)
}
