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

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

const serverName = "Chi HTTP Server"

// Middleware returns a chi middleware that creates a pinpoint.Tracer that instruments the http handler.
func Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return wrap(next, "chi.HandlerFunc()")
	}
}

func wrap(handler http.Handler, funcName string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !pinpoint.GetAgent().Enable() {
			handler.ServeHTTP(w, r)
			return
		}

		status := http.StatusOK
		tracer := pphttp.NewHttpServerTracer(r, serverName)

		defer tracer.EndSpan()
		defer func() {
			tracer.CollectUrlStat(chi.RouteContext(r.Context()).RoutePattern(), status)
			pphttp.RecordHttpServerResponse(tracer, status, w.Header())
		}()
		defer func() {
			if e := recover(); e != nil {
				status = http.StatusInternalServerError
				panic(e)
			}
		}()
		defer tracer.NewSpanEvent(funcName).EndSpanEvent()

		w = pphttp.WrapResponseWriter(w, &status)
		r = pinpoint.RequestWithTracerContext(r, tracer)
		handler.ServeHTTP(w, r)
	})
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
