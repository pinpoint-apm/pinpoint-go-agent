// Package ppfasthttprouter instruments the fasthttp/router package (https://github.com/fasthttp/router).
//
// This package instruments inbound requests handled by a fasthttp/router.Router.
// Use New() to trace all handlers:
//
//	r := ppfasthttprouter.New()
//	r.GET("/", Index)
//	r.GET("/hello/:name", Hello)
package ppfasthttprouter

import (
	"io/fs"

	"github.com/fasthttp/router"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/fasthttp/v2"
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

// Group returns a group whose routes are instrumented like the Router's.
// fasthttp/router's own Group registers on the router underneath, past this
// package's wrapping, so its routes ran untraced.
func (r *Router) Group(path string) *Group {
	return &Group{group: r.Router.Group(path), prefix: path}
}

// Group is a fasthttp/router.Group whose routes are instrumented. The URL
// statistics get the full pattern, the group's prefix and the route's path,
// as the router registers it.
type Group struct {
	group  *router.Group
	prefix string
}

// Group returns a nested group whose routes are instrumented.
func (g *Group) Group(path string) *Group {
	sub := g.group.Group(path)
	if sub == g.group { // fasthttp/router returns the group itself for "/"
		return g
	}
	return &Group{group: sub, prefix: g.prefix + path}
}

func (g *Group) wrap(path string, handler fasthttp.RequestHandler) fasthttp.RequestHandler {
	return ppfasthttp.WrapHandler(handler, g.prefix+path)
}

func (g *Group) GET(path string, handler fasthttp.RequestHandler) {
	g.group.GET(path, g.wrap(path, handler))
}

func (g *Group) HEAD(path string, handler fasthttp.RequestHandler) {
	g.group.HEAD(path, g.wrap(path, handler))
}

func (g *Group) POST(path string, handler fasthttp.RequestHandler) {
	g.group.POST(path, g.wrap(path, handler))
}

func (g *Group) PUT(path string, handler fasthttp.RequestHandler) {
	g.group.PUT(path, g.wrap(path, handler))
}

func (g *Group) PATCH(path string, handler fasthttp.RequestHandler) {
	g.group.PATCH(path, g.wrap(path, handler))
}

func (g *Group) DELETE(path string, handler fasthttp.RequestHandler) {
	g.group.DELETE(path, g.wrap(path, handler))
}

func (g *Group) CONNECT(path string, handler fasthttp.RequestHandler) {
	g.group.CONNECT(path, g.wrap(path, handler))
}

func (g *Group) OPTIONS(path string, handler fasthttp.RequestHandler) {
	g.group.OPTIONS(path, g.wrap(path, handler))
}

func (g *Group) TRACE(path string, handler fasthttp.RequestHandler) {
	g.group.TRACE(path, g.wrap(path, handler))
}

func (g *Group) ANY(path string, handler fasthttp.RequestHandler) {
	g.group.ANY(path, g.wrap(path, handler))
}

func (g *Group) Handle(method, path string, handler fasthttp.RequestHandler) {
	g.group.Handle(method, path, g.wrap(path, handler))
}

// ServeFiles, ServeFS and ServeFilesCustom serve files untraced, as the
// Router's do.
func (g *Group) ServeFiles(path string, rootPath string) {
	g.group.ServeFiles(path, rootPath)
}

func (g *Group) ServeFS(path string, filesystem fs.FS) {
	g.group.ServeFS(path, filesystem)
}

func (g *Group) ServeFilesCustom(path string, custom *fasthttp.FS) {
	g.group.ServeFilesCustom(path, custom)
}
