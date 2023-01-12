// Package ppfasthttprouter instruments the fasthttp/router package (https://github.com/fasthttp/router).
//
// This package instruments inbound requests handled by a fasthttp/router.Router.
// Use New() to trace all handlers:
//
//	r := ppfasthttprouter.New()
//	r.GET("/", Index)
//	r.GET("/hello/:name", Hello)
//
package ppfasthttprouter

import (
	"github.com/fasthttp/router"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp"
	"github.com/valyala/fasthttp"
)

type Router struct {
	*router.Router
}

// New returns a new Router which will instrument all added fasthttp/router.Router handlers.
func New() *Router {
	return &Router{
		Router: router.New(),
	}
}

func (r *Router) GET(path string, handler fasthttp.RequestHandler) {
	r.Router.GET(path, ppfasthttp.WrapHandler(handler, path))
}

func (r *Router) HEAD(path string, handler fasthttp.RequestHandler) {
	r.Router.HEAD(path, ppfasthttp.WrapHandler(handler, path))
}

func (r *Router) POST(path string, handler fasthttp.RequestHandler) {
	r.Router.POST(path, ppfasthttp.WrapHandler(handler, path))
}

func (r *Router) PUT(path string, handler fasthttp.RequestHandler) {
	r.Router.PUT(path, ppfasthttp.WrapHandler(handler, path))
}

func (r *Router) PATCH(path string, handler fasthttp.RequestHandler) {
	r.Router.PATCH(path, ppfasthttp.WrapHandler(handler, path))
}

func (r *Router) DELETE(path string, handler fasthttp.RequestHandler) {
	r.Router.DELETE(path, ppfasthttp.WrapHandler(handler, path))
}

func (r *Router) CONNECT(path string, handler fasthttp.RequestHandler) {
	r.Router.CONNECT(path, ppfasthttp.WrapHandler(handler, path))
}

func (r *Router) OPTIONS(path string, handler fasthttp.RequestHandler) {
	r.Router.OPTIONS(path, ppfasthttp.WrapHandler(handler, path))
}

func (r *Router) TRACE(path string, handler fasthttp.RequestHandler) {
	r.Router.TRACE(path, ppfasthttp.WrapHandler(handler, path))
}

func (r *Router) ANY(path string, handler fasthttp.RequestHandler) {
	r.Router.ANY(path, ppfasthttp.WrapHandler(handler, path))
}

func (r *Router) Handle(method, path string, handler fasthttp.RequestHandler) {
	r.Router.Handle(method, path, ppfasthttp.WrapHandler(handler, path))
}
