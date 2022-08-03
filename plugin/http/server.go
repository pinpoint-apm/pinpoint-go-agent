package http

import (
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	"net"
	"net/http"
	"strconv"
	"strings"
)

const AnnotationProxyHttpHeader = 300

func NewHttpServerTracer(agent pinpoint.Agent, req *http.Request, operation string) pinpoint.Tracer {
	tracer := agent.NewSpanTracerWithReader(operation, req.URL.Path, req.Header)
	tracer.Span().SetEndPoint(req.Host)
	tracer.Span().SetRemoteAddress(getRemoteAddr(req))
	setProxyHeader(tracer, req)

	return tracer
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

func setProxyHeader(tracer pinpoint.Tracer, r *http.Request) {
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
		tracer.Span().Annotations().AppendLongIntIntByteByteString(AnnotationProxyHttpHeader, receivedTime, code, int32(durationTime), int32(idlePercent), int32(busyPercent), app)
	}
}

func TraceHttpStatus(tracer pinpoint.Tracer, status int) {
	tracer.Span().Annotations().AppendInt(pinpoint.AnnotationHttpStatusCode, int32(status))
	tracer.Span().SetHttpStatusCode(status)
}

func WrapHandle(agent pinpoint.Agent, handlerName string, pattern string, handler http.Handler) (string, http.Handler) {
	return pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if agent == nil || !agent.Enable() {
			handler.ServeHTTP(w, r)
			return
		}

		tracer := NewHttpServerTracer(agent, r, "Http Server")
		defer tracer.EndSpan()
		defer tracer.NewSpanEvent(handlerName).EndSpanEvent()

		status := http.StatusOK
		w = WrapResponseWriter(w, &status)
		r = pinpoint.RequestWithTracerContext(r, tracer)
		handler.ServeHTTP(w, r)
		TraceHttpStatus(tracer, status)
	})
}

func WrapHandleFunc(agent pinpoint.Agent, handlerName string, pattern string, handler func(http.ResponseWriter, *http.Request)) (string, func(http.ResponseWriter, *http.Request)) {
	p, h := WrapHandle(agent, handlerName, pattern, http.HandlerFunc(handler))
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
