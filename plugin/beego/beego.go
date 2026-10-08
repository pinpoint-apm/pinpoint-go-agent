// Package ppbeego instruments the beego/v2 package (https://github.com/beego/beego).
//
// This package instruments inbound requests handled by a beego instance.
// Register the ServerFilterChain as the filter chain of the router to trace all handlers:
//
//	web.InsertFilterChain("/*", ppbeego.ServerFilterChain())
//
// This package instruments outbound requests and add distributed tracing headers.
// Add the ClientFilterChain as the filter chain of the request:
//
//	req := httplib.Get("http://localhost:9090/")
//	req.AddFilters(ppbeego.ClientFilterChain(tracer))
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
					pphttp.CollectUrlStat(tracer, routerPattern, r.Method, status)
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
// The filter records on tracer, so add it per request (req.AddFilters) rather
// than on settings shared by every request, which would record every caller's
// request on the one tracer. A nil tracer reads the request's own, from the
// context the filter is called with, which is the form that is safe to share.
func ClientFilterChain(tracer pinpoint.Tracer) func(httplib.Filter) httplib.Filter {
	return func(next httplib.Filter) httplib.Filter {
		return func(ctx context.Context, req *httplib.BeegoHTTPRequest) (resp *http.Response, err error) {
			if tracer == nil {
				tracer = pinpoint.FromContext(ctx)
			}
			// See DoRequest for why the returned tracer ends the event.
			t := pphttp.NewHttpClientTracer(tracer, "beego/v2.DoRequest()", req.GetRequest())
			defer func() {
				pphttp.EndHttpClientTracer(t, resp, err)
			}()
			resp, err = next(ctx, req)
			return
		}
	}
}
