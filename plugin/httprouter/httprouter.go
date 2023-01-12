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
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

const serverName = "HttpRouter Server"

// WrapHandle wraps the given httprouter handler and adds the pinpoint.Tracer to the request's context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
func WrapHandle(handler httprouter.Handle) httprouter.Handle {
	return wrapHandle(handler, "")
}

func wrapHandle(handler httprouter.Handle, path string) httprouter.Handle {
	return wrapHandleWithName(handler, pphttp.HandlerFuncName(handler), path)
}

func wrapHandleWithName(handler httprouter.Handle, handlerName string, path string) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		if !pinpoint.GetAgent().Enable() {
			handler(w, r, p)
			return
		}

		status := http.StatusOK
		tracer := pphttp.NewHttpServerTracer(r, serverName)

		defer tracer.EndSpan()
		defer func() {
			if path != "" {
				pphttp.CollectUrlStat(tracer, path, status)
			}
			pphttp.RecordHttpServerResponse(tracer, status, w.Header())
		}()
		defer func() {
			if e := recover(); e != nil {
				status = http.StatusInternalServerError
				panic(e)
			}
		}()

		defer tracer.NewSpanEvent(handlerName).EndSpanEvent()
		w = pphttp.WrapResponseWriter(w, &status)
		r = pinpoint.RequestWithTracerContext(r, tracer)
		handler(w, r, p)
	}
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
