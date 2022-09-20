package gorilla

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

func Middleware() mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !pinpoint.GetAgent().Enable() {
				next.ServeHTTP(w, r)
				return
			}

			tracer := phttp.NewHttpServerTracer(r, "Gorilla/Mux Server")
			defer tracer.EndSpan()
			if !tracer.IsSampled() {
				next.ServeHTTP(w, r)
				return
			}

			var handler interface{}
			if route := mux.CurrentRoute(r); route != nil {
				handler = route.GetHandler()
			} else {
				handler = next
			}
			tracer.NewSpanEvent(phttp.HandlerFuncName(handler))
			defer tracer.EndSpanEvent()

			status := http.StatusOK
			w = phttp.WrapResponseWriter(w, &status)
			r = pinpoint.RequestWithTracerContext(r, tracer)

			next.ServeHTTP(w, r)
			phttp.RecordHttpServerResponse(tracer, status, w.Header())
		})
	}
}
