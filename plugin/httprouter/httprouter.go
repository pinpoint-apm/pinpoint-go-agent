package pphttprouter

import (
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

const serverName = "HttpRouter Server"

// WrapHandle wraps the given httprouter handler and adds the pinpoint.Tracer to the request's context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
func WrapHandle(handler httprouter.Handle) httprouter.Handle {
	funcName := pphttp.HandlerFuncName(handler)

	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		if !pinpoint.GetAgent().Enable() {
			handler(w, r, p)
			return
		}

		tracer := pphttp.NewHttpServerTracer(r, serverName)
		defer tracer.EndSpan()

		if !tracer.IsSampled() {
			handler(w, r, p)
			return
		}
		defer func() {
			if e := recover(); e != nil {
				status := http.StatusInternalServerError
				pphttp.RecordHttpServerResponse(tracer, status, w.Header())
				panic(e)
			}
		}()

		tracer.NewSpanEvent(funcName)
		defer tracer.EndSpanEvent()

		status := http.StatusOK
		w = pphttp.WrapResponseWriter(w, &status)
		r = pinpoint.RequestWithTracerContext(r, tracer)

		handler(w, r, p)
		pphttp.RecordHttpServerResponse(tracer, status, w.Header())
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
	r.Router.GET(path, WrapHandle(handle))
}

func (r *Router) HEAD(path string, handle httprouter.Handle) {
	r.Router.HEAD(path, WrapHandle(handle))
}

func (r *Router) OPTIONS(path string, handle httprouter.Handle) {
	r.Router.OPTIONS(path, WrapHandle(handle))
}

func (r *Router) POST(path string, handle httprouter.Handle) {
	r.Router.POST(path, WrapHandle(handle))
}

func (r *Router) PUT(path string, handle httprouter.Handle) {
	r.Router.PUT(path, WrapHandle(handle))
}

func (r *Router) PATCH(path string, handle httprouter.Handle) {
	r.Router.PATCH(path, WrapHandle(handle))
}

func (r *Router) DELETE(path string, handle httprouter.Handle) {
	r.Router.DELETE(path, WrapHandle(handle))
}

func (r *Router) Handle(method, path string, handle httprouter.Handle) {
	r.Router.Handle(method, path, WrapHandle(handle))
}

func (r *Router) HandlerFunc(method, path string, handler http.HandlerFunc) {
	r.Router.HandlerFunc(method, path, pphttp.WrapHandlerFunc(handler, serverName))
}

func (r *Router) Handler(method, path string, handler http.Handler) {
	r.Router.Handler(method, path, pphttp.WrapHandler(handler, serverName))
}
