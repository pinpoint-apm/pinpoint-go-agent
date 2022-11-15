// Package ppbeego instruments the beego/v2 package (https://github.com/beego/beego).
//
// This package instruments inbound requests handled by a beego instance.
// Register the Middleware as the middleware of the router to trace all handlers:
//
//  web.RunWithMiddleWares("localhost:8080", ppbeego.Middleware())
//
// This package instruments outbound requests and add distributed tracing headers.
// Use DoRequest.
//
//  req := httplib.Get("http://localhost:9090/")
//  ppbeego.DoRequest(tracer, req)
//
package ppbeego

import (
	"net/http"

	"github.com/beego/beego/v2/client/httplib"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

const serverName = "Beego Server"

// Middleware returns middleware that will trace incoming requests.
func Middleware() func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !pinpoint.GetAgent().Enable() {
				h.ServeHTTP(w, r)
				return
			}

			tracer := pphttp.NewHttpServerTracer(r, serverName)
			defer tracer.EndSpan()

			if !tracer.IsSampled() {
				h.ServeHTTP(w, r)
				return
			}
			defer func() {
				if e := recover(); e != nil {
					status := http.StatusInternalServerError
					pphttp.RecordHttpServerResponse(tracer, status, w.Header())
					panic(e)
				}
			}()

			tracer.NewSpanEvent("beegov2.HandlerFunc()")
			defer tracer.EndSpanEvent()

			status := http.StatusOK
			w = pphttp.WrapResponseWriter(w, &status)
			r = pinpoint.RequestWithTracerContext(r, tracer)

			h.ServeHTTP(w, r)
			pphttp.RecordHttpServerResponse(tracer, status, w.Header())
		})
	}
}

// DoRequest instruments and executes a given request.
func DoRequest(tracer pinpoint.Tracer, req *httplib.BeegoHTTPRequest) (*http.Response, error) {
	pphttp.NewHttpClientTracer(tracer, "beegov2.DoRequest()", req.GetRequest())
	resp, err := req.DoRequest()
	pphttp.EndHttpClientTracer(tracer, resp, err)
	return resp, err
}
