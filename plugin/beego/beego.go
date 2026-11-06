// Package ppbeego instruments the beego/v2 package (https://github.com/beego/beego).
//
// This package instruments inbound requests handled by a beego instance.
// Register the ServerFilterChain as the filter chain of the router to trace all handlers:
//
//	web.InsertFilterChain("/*", ppbeego.ServerFilterChain())
//
// This package instruments outbound requests and adds distributed tracing headers.
// Register ClientFilterChain(nil) once, and build each request with a context
// carrying the tracer:
//
//	httplib.AddDefaultFilter(ppbeego.ClientFilterChain(nil)) // at startup
//
//	req := httplib.NewBeegoRequestWithCtx(pinpoint.NewContext(ctx, tracer), "http://localhost:9090/", "GET")
package ppbeego

import (
	"context"
	"net/http"

	"github.com/beego/beego/v2/client/httplib"
	"github.com/beego/beego/v2/server/web"
	beegoContext "github.com/beego/beego/v2/server/web/context"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

const serverName = "Beego Server"

// ServerFilterChain returns filter function that will trace the incoming requests.
func ServerFilterChain() func(web.FilterFunc) web.FilterFunc {
	return func(next web.FilterFunc) web.FilterFunc {
		return func(ctx *beegoContext.Context) {
			if !pinpoint.GetAgent().Enable() {
				next(ctx)
				return
			}

			r := ctx.Request
			tracer := pphttp.NewHttpServerTracer(r, serverName)
			ctx.Request = pinpoint.RequestWithTracerContext(r, tracer)
			pphttp.TraceSpan(tracer, "beego/v2.HandlerFunc()", func() int {
				next(ctx)
				return responseStatus(ctx)
			}, func(status int) {
				// GetData takes a mutex and probes a map per call, so don't
				// pay for it when the stat would be dropped anyway.
				if pphttp.IsUrlStatEnabled() {
					routerPattern := ""
					// Comma-ok: Input.GetData is an interface{} store keyed by
					// string, so anything the application put under the same key
					// panicked the deferred stat collection.
					if rp, ok := ctx.Input.GetData("RouterPattern").(string); ok {
						routerPattern = rp
					}
					pphttp.CollectUrlStat(tracer, routerPattern, r.Method)
				}
				pphttp.RecordHttpServerResponse(tracer, status, ctx.ResponseWriter.Header())
			})
		}
	}
}

// responseStatus is the status the response went out with. The writer's
// Status is what beego's own access log reads: Output.Body, behind ServeJSON,
// Render and the rest, writes the header and then resets Output.Status to 0,
// and Redirect and plain writes never set it, so reading Output.Status alone
// recorded 0 for nearly every response. Output.Status still covers a handler
// that set a status and wrote nothing; a response nothing wrote to is a 200.
func responseStatus(ctx *beegoContext.Context) int {
	if status := ctx.ResponseWriter.Status; status != 0 {
		return status
	}
	if status := ctx.Output.Status; status != 0 {
		return status
	}
	return http.StatusOK
}

// ClientFilterChain returns filter function that will trace the outgoing requests.
// A nil tracer reads each request's own, from the context the filter is called
// with: register it once with httplib.AddDefaultFilter and build requests with
// httplib.NewBeegoRequestWithCtx. Do not add a filter with a tracer through
// req.AddFilters: httplib copies its default settings into every request,
// spare capacity included, so AddFilters appends into an array the requests
// share, and a concurrent request runs that filter, recording on that tracer
// and sending its trace id, instead of its own.
func ClientFilterChain(tracer pinpoint.Tracer) func(httplib.Filter) httplib.Filter {
	return func(next httplib.Filter) httplib.Filter {
		return func(ctx context.Context, req *httplib.BeegoHTTPRequest) (resp *http.Response, err error) {
			// A local, never the captured parameter: assigning the request's
			// tracer to the closure variable pinned the first request's tracer
			// on every later request through a shared filter chain, and the
			// write raced with the other requests reading it.
			reqTracer := tracer
			if reqTracer == nil {
				reqTracer = pinpoint.FromContext(ctx)
			}
			// The event is ended through the tracer NewHttpClientTracer
			// returns, not reqTracer: for a request that already carries this
			// span's context it is a noop, and ending reqTracer would close
			// whatever event the caller had open.
			t := pphttp.NewHttpClientTracer(reqTracer, "beego/v2.DoRequest()", req.GetRequest())
			defer func() {
				pphttp.EndHttpClientTracer(t, resp, err)
			}()
			resp, err = next(ctx, req)
			return
		}
	}
}
