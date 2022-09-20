package chi

import (
	"net/http"

	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			if !pinpoint.GetAgent().Enable() {
				next.ServeHTTP(w, r)
				return
			}

			tracer := phttp.NewHttpServerTracer(r, "Chi Server")
			defer tracer.EndSpan()

			if !tracer.IsSampled() {
				next.ServeHTTP(w, r)
				return
			}

			tracer.NewSpanEvent(phttp.HandlerFuncName(next))
			defer tracer.EndSpanEvent()

			status := http.StatusOK
			w = phttp.WrapResponseWriter(w, &status)
			r = pinpoint.RequestWithTracerContext(r, tracer)

			next.ServeHTTP(w, r)
			phttp.RecordHttpServerResponse(tracer, status, w.Header())
		}
		return http.HandlerFunc(fn)
	}
}
