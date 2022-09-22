package chi

import (
	"net/http"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

const serverName = "Chi HTTP Server"

func Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !pinpoint.GetAgent().Enable() {
				next.ServeHTTP(w, r)
				return
			}

			tracer := phttp.NewHttpServerTracer(r, serverName)
			defer tracer.EndSpan()

			if !tracer.IsSampled() {
				next.ServeHTTP(w, r)
				return
			}

			tracer.NewSpanEvent("chi.HandlerFunc()")
			defer tracer.EndSpanEvent()

			status := http.StatusOK
			w = phttp.WrapResponseWriter(w, &status)
			r = pinpoint.RequestWithTracerContext(r, tracer)

			next.ServeHTTP(w, r)
			phttp.RecordHttpServerResponse(tracer, status, w.Header())
		})
	}
}

func WrapHandler(handler http.Handler) http.Handler {
	return phttp.WrapHandler(handler, serverName)
}

func WrapHandlerFunc(f func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	return phttp.WrapHandlerFunc(f, serverName)
}
