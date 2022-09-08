package http

import (
	"bytes"
	"net"
	"net/http"
	"reflect"
	"runtime"
	"strconv"
	"strings"

	"github.com/pinpoint-apm/pinpoint-go-agent"
)

func NewHttpServerTracer(agent pinpoint.Agent, req *http.Request, operation string) pinpoint.Tracer {
	if isExcludedUrl(req.URL.Path) || isExcludedMethod(req.Method) {
		return pinpoint.NoopTracer()
	} else {
		tracer := agent.NewSpanTracerWithReader(operation, req.URL.Path, req.Header)

		span := tracer.Span()
		span.SetEndPoint(req.Host)
		span.SetRemoteAddress(getRemoteAddr(req))

		if tracer.IsSampled() {
			a := span.Annotations()
			recordServerHttpRequestHeader(a, req.Header)
			recordServerHttpCookie(a, req.Cookies())
			setProxyHeader(a, req)
		}

		return tracer
	}
}

func getRemoteAddr(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if parts := strings.Split(xff, ","); len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}

	if xff := r.Header.Get("X-Real-Ip"); xff != "" {
		if parts := strings.Split(xff, ","); len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}

	addr, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return addr
	}

	return r.RemoteAddr
}

func setProxyHeader(a pinpoint.Annotation, r *http.Request) {
	var receivedTime int64
	var durationTime, idlePercent, busyPercent int
	var code int32 = 0
	var app = ""

	if xff := r.Header.Get("Pinpoint-ProxyApache"); xff != "" {
		parts := strings.Split(xff, " ")
		for _, str := range parts {
			e := strings.Split(str, "=")
			if e[0] == "t" {
				receivedTime, _ = strconv.ParseInt(e[1], 10, 64)
				receivedTime = receivedTime / 1000
			} else if e[0] == "D" {
				durationTime, _ = strconv.Atoi(e[1])
			} else if e[0] == "i" {
				idlePercent, _ = strconv.Atoi(e[1])
			} else if e[0] == "b" {
				busyPercent, _ = strconv.Atoi(e[1])
			}
		}
		code = 3
	} else if xff := r.Header.Get("Pinpoint-ProxyNginx"); xff != "" {
		parts := strings.Split(xff, " ")
		for _, str := range parts {
			e := strings.Split(str, "=")
			if e[0] == "t" {
				tmp, _ := strconv.ParseFloat(e[1], 64)
				tmp = tmp * 1000
				receivedTime = int64(tmp)
			} else if e[0] == "D" {
				durationTime, _ = strconv.Atoi(e[1])
			}
		}
		code = 2
	} else if xff := r.Header.Get("Pinpoint-ProxyApp"); xff != "" {
		parts := strings.Split(xff, " ")
		for _, str := range parts {
			e := strings.Split(str, "=")
			if e[0] == "t" {
				receivedTime, _ = strconv.ParseInt(e[1], 10, 64)
			} else if e[0] == "app" {
				app = e[1]
			}
		}
		code = 1
	}

	if code > 0 {
		a.AppendLongIntIntByteByteString(pinpoint.AnnotationProxyHttpHeader, receivedTime, code, int32(durationTime),
			int32(idlePercent), int32(busyPercent), app)
	}
}

func RecordHttpServerResponse(tracer pinpoint.Tracer, status int, header http.Header) {
	if tracer.IsSampled() {
		recordServerHttpStatus(tracer.Span(), status)
		recordServerHttpResponseHeader(tracer.Span().Annotations(), header)
	}
}

func WrapHandle(agent pinpoint.Agent, pattern string, handler http.Handler) (string, http.Handler) {
	return pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if agent == nil || !agent.Enable() {
			handler.ServeHTTP(w, r)
			return
		}

		tracer := NewHttpServerTracer(agent, r, "Http Server")
		defer tracer.EndSpan()
		defer tracer.NewSpanEvent("http/ServeMux.ServeHTTP(ResponseWriter, Request)").EndSpanEvent()
		defer tracer.NewSpanEvent(makeMethodName(handler)).EndSpanEvent()

		status := http.StatusOK
		w = WrapResponseWriter(w, &status)
		r = pinpoint.RequestWithTracerContext(r, tracer)

		handler.ServeHTTP(w, r)
		RecordHttpServerResponse(tracer, status, w.Header())
	})
}

func WrapHandleFunc(agent pinpoint.Agent, pattern string, handler func(http.ResponseWriter, *http.Request)) (string, func(http.ResponseWriter, *http.Request)) {
	p, h := WrapHandle(agent, pattern, http.HandlerFunc(handler))
	return p, func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }
}

type responseWriter struct {
	http.ResponseWriter
	status *int
}

func WrapResponseWriter(w http.ResponseWriter, status *int) *responseWriter {
	return &responseWriter{w, status}
}

func (w *responseWriter) WriteHeader(status int) {
	w.ResponseWriter.WriteHeader(status)
	*w.status = status
}

type ServeMux struct {
	*http.ServeMux
	agent pinpoint.Agent
}

func NewServeMux(agent pinpoint.Agent) *ServeMux {
	return &ServeMux{
		ServeMux: http.NewServeMux(),
		agent:    agent,
	}
}

func (mux *ServeMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if mux.agent == nil || !mux.agent.Enable() {
		mux.ServeMux.ServeHTTP(w, r)
		return
	}

	tracer := NewHttpServerTracer(mux.agent, r, "Http Server")
	defer tracer.EndSpan()
	defer tracer.NewSpanEvent("http/ServeMux.ServeHTTP(ResponseWriter, Request)").EndSpanEvent()

	handler, _ := mux.Handler(r)
	defer tracer.NewSpanEvent(makeMethodName(handler)).EndSpanEvent()

	status := http.StatusOK
	w = WrapResponseWriter(w, &status)
	r = pinpoint.RequestWithTracerContext(r, tracer)

	mux.ServeMux.ServeHTTP(w, r)
	RecordHttpServerResponse(tracer, status, w.Header())

}

func makeMethodName(f interface{}) string {
	var buf bytes.Buffer
	buf.WriteString(runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name())
	buf.WriteString("(ResponseWriter, Request)")
	return buf.String()
}
