// Package ppbeego instruments the beego/v2 package (https://github.com/beego/beego).
//
// This package instruments inbound requests handled by a beego instance.
// Register the ServerFilterChain as the filter chain of the router to trace all handlers:
//
//  web.InsertFilterChain("/*", ppbeego.ServerFilterChain())
//
// This package instruments outbound requests and add distributed tracing headers.
// Add the ClientFilterChain as the filter chain of the request:
//
//  req := httplib.Get("http://localhost:9090/")
//  req.AddFilters(ppbeego.ClientFilterChain(tracer))
//
package ppbeego

import (
	"context"
	"net/http"

	"github.com/beego/beego/v2/client/httplib"
	"github.com/beego/beego/v2/server/web"
	beegoContext "github.com/beego/beego/v2/server/web/context"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

const serverName = "Beego Server"

// Middleware is deprecated. Use ServerFilterChain.
func Middleware() func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !pinpoint.GetAgent().Enable() {
				h.ServeHTTP(w, r)
				return
			}

			status := http.StatusOK
			tracer := pphttp.NewHttpServerTracer(r, serverName)

			defer tracer.EndSpan()
			defer func() {
				pphttp.RecordHttpServerResponse(tracer, status, w.Header())
			}()
			defer func() {
				if e := recover(); e != nil {
					status = http.StatusInternalServerError
					panic(e)
				}
			}()
			defer tracer.NewSpanEvent("beego/v2.HandlerFunc()").EndSpanEvent()

			w = pphttp.WrapResponseWriter(w, &status)
			r = pinpoint.RequestWithTracerContext(r, tracer)
			h.ServeHTTP(w, r)
		})
	}
}

// ServerFilterChain returns filter function that will trace the incoming requests.
func ServerFilterChain() func(web.FilterFunc) web.FilterFunc {
	return func(next web.FilterFunc) web.FilterFunc {
		return func(ctx *beegoContext.Context) {
			if !pinpoint.GetAgent().Enable() {
				next(ctx)
				return
			}

			r := ctx.Request
			status := http.StatusOK
			tracer := pphttp.NewHttpServerTracer(r, serverName)

			defer tracer.EndSpan()
			defer func() {
				routerPattern := ""
				if rp := ctx.Input.GetData("RouterPattern"); rp != nil {
					routerPattern = rp.(string)
				}
				tracer.CollectUrlStat(routerPattern, status)
				pphttp.RecordHttpServerResponse(tracer, status, ctx.ResponseWriter.Header())
			}()
			defer func() {
				if e := recover(); e != nil {
					status = http.StatusInternalServerError
					panic(e)
				}
			}()
			defer tracer.NewSpanEvent("beego/v2.HandlerFunc()").EndSpanEvent()

			ctx.Request = pinpoint.RequestWithTracerContext(r, tracer)
			next(ctx)
			status = ctx.Output.Status
		}
	}
}

// DoRequest is deprecated. Use ClientFilterChain.
func DoRequest(tracer pinpoint.Tracer, req *httplib.BeegoHTTPRequest) (resp *http.Response, err error) {
	pphttp.NewHttpClientTracer(tracer, "beego/v2.DoRequest()", req.GetRequest())
	defer func() {
		pphttp.EndHttpClientTracer(tracer, resp, err)
	}()
	resp, err = req.DoRequest()
	return
}

// ClientFilterChain returns filter function that will trace the outgoing requests.
func ClientFilterChain(tracer pinpoint.Tracer) func(httplib.Filter) httplib.Filter {
	return func(next httplib.Filter) httplib.Filter {
		return func(ctx context.Context, req *httplib.BeegoHTTPRequest) (resp *http.Response, err error) {
			pphttp.NewHttpClientTracer(tracer, "beego/v2.DoRequest()", req.GetRequest())
			defer func() {
				pphttp.EndHttpClientTracer(tracer, resp, err)
			}()
			resp, err = next(ctx, req)
			return
		}
	}
}
