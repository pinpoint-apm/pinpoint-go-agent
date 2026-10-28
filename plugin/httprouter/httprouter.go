// Package pphttprouter instruments the julienschmidt/httprouter package (https://github.com/julienschmidt/httprouter).
//
// This package instruments inbound requests handled by a httprouter.Router.
// Use New() to trace all handlers:
//
//	r := pphttprouter.New()
//	r.GET("/", Index)
//	r.GET("/hello/:name", Hello)
//
// Use WrapHandle to select the handlers you want to track:
//
//	r := httprouter.New()
//	r.GET("/", Index)
//	r.GET("/hello/:name", pphttprouter.WrapHandle(hello))
package pphttprouter

import (
	"context"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
)

const serverName = "HttpRouter Server"

// WrapHandle wraps the given httprouter handler and adds the pinpoint.Tracer to the request's context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
// The optional path is the route pattern the handler is registered under
// ("/hello/:name"); with it the URL statistics are collected as the Router of
// this package does, without it they are skipped. The compile-time
// instrumentation tool passes it from the registration it hooks.
func WrapHandle(handler httprouter.Handle, path ...string) httprouter.Handle {
	if len(path) > 0 {
		return wrapHandle(handler, path[0])
	}
	return wrapHandle(handler, "")
}

func wrapHandle(handler httprouter.Handle, path string) httprouter.Handle {
	return wrapHandleWithName(handler, pphttp.HandlerFuncName(handler), path)
}

// wrapHandleWithName is pphttp.TraceHandler for a httprouter.Handle, whose
// third argument keeps it from being an http.Handler: the handler is wrapped
// into one per request, with the route's Params bound.
func wrapHandleWithName(handler httprouter.Handle, handlerName string, path string) httprouter.Handle {
	var urlPattern func(*http.Request) string
	if path != "" {
		urlPattern = func(*http.Request) string { return path }
	}
	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		pphttp.ServeTraced(w, r, paramsHandler{handler, p}, serverName, handlerName, urlPattern)
	}
}

// paramsHandler is a httprouter.Handle with its route Params bound, as the
// http.Handler ServeTraced serves. A value, not a closure: the closure and
// the per-request http.HandlerFunc TraceHandler built around it were two heap
// allocations per request; boxing this value into the interface is one.
type paramsHandler struct {
	handler httprouter.Handle
	p       httprouter.Params
}

func (h paramsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handler(w, r, h.p)
}

type Router struct {
	*httprouter.Router
}

// New returns a new Router which will instrument all added httprouter.Router handlers.
func New() *Router {
	return &Router{
		Router: httprouter.New(),
	}
}

func (r *Router) GET(path string, handle httprouter.Handle) {
	r.Router.GET(path, wrapHandle(handle, path))
}

func (r *Router) HEAD(path string, handle httprouter.Handle) {
	r.Router.HEAD(path, wrapHandle(handle, path))
}

func (r *Router) OPTIONS(path string, handle httprouter.Handle) {
	r.Router.OPTIONS(path, wrapHandle(handle, path))
}

func (r *Router) POST(path string, handle httprouter.Handle) {
	r.Router.POST(path, wrapHandle(handle, path))
}

func (r *Router) PUT(path string, handle httprouter.Handle) {
	r.Router.PUT(path, wrapHandle(handle, path))
}

func (r *Router) PATCH(path string, handle httprouter.Handle) {
	r.Router.PATCH(path, wrapHandle(handle, path))
}

func (r *Router) DELETE(path string, handle httprouter.Handle) {
	r.Router.DELETE(path, wrapHandle(handle, path))
}

func (r *Router) Handle(method, path string, handle httprouter.Handle) {
	r.Router.Handle(method, path, wrapHandle(handle, path))
}

func (r *Router) Handler(method, path string, handler http.Handler) {
	f := func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		if len(p) > 0 {
			ctx := context.WithValue(r.Context(), httprouter.ParamsKey, p)
			r = r.WithContext(ctx)
		}
		handler.ServeHTTP(w, r)
	}
	r.Router.Handle(method, path, wrapHandleWithName(f, pphttp.HandlerFuncName(handler), path))
}

func (r *Router) HandlerFunc(method, path string, handler http.HandlerFunc) {
	r.Handler(method, path, handler)
}
