package chi

import (
	"net/http"

	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func Middleware(agent pinpoint.Agent) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			if agent == nil || !agent.Enable() {
				next.ServeHTTP(w, r)
				return
			}

			apiId := agent.RegisterSpanApiId("Go Chi Server", pinpoint.ApiTypeWebRequest)

			tracer := phttp.NewHttpServerTracer(agent, r, "Chi Server")
			defer tracer.EndSpan()
			tracer.Span().SetApiId(apiId)

			routePath := r.URL.Path
			if r.URL.RawPath != "" {
				routePath = r.URL.RawPath
			}
			defer tracer.NewSpanEvent(routePath).EndSpanEvent()

			status := http.StatusOK
			w = phttp.WrapResponseWriter(w, &status)
			r = pinpoint.RequestWithTracerContext(r, tracer)

			next.ServeHTTP(w, r)
			phttp.TraceHttpStatus(tracer, status)
		}
		return http.HandlerFunc(fn)
	}
}
